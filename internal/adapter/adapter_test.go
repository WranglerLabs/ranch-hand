package adapter

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/WranglerLabs/ranch-hand/internal/plan"
	"golang.org/x/crypto/ssh"
)

func targetPlan(target string, configuration map[string]string) plan.DeploymentPlan {
	return plan.DeploymentPlan{
		SchemaVersion: plan.CurrentSchemaVersion, Name: "Test Wrangler",
		Release: plan.ReleaseSelection{
			Version: "v1.2.3", ManifestURL: "https://github.com/WranglerLabs/repo-wrangler/releases/download/v1.2.3/release-manifest.json",
			ManifestSHA256: strings.Repeat("a", 64), ArtifactSHA256: strings.Repeat("b", 64), ArtifactSize: 42,
		},
		Target: plan.Target{Kind: target}, Configuration: configuration,
	}
}

func TestAzureContainerAppsUsesARMAndRequiresRegisteredProvider(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("Authorization") != "Bearer azure-token" {
			t.Fatal("missing ARM bearer token")
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "Microsoft.App") {
			_, _ = io.WriteString(w, `{"registrationState":"Registered"}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"subscription","state":"Enabled"}`)
	}))
	defer server.Close()
	adapter := newAzureContainerApps(server.Client(), server.URL)
	report := adapter.Preflight(context.Background(), targetPlan("azure-container-apps", map[string]string{"subscriptionId": "sub"}), Credentials{AzureAccessToken: "azure-token"})
	if !report.Ready || requests != 2 {
		t.Fatalf("unexpected ARM preflight: %+v; requests=%d", report, requests)
	}
}

func TestCloudflareVerifiesTokenAndAccount(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer cf-token" {
			t.Fatal("missing Cloudflare bearer token")
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/user/tokens/verify") {
			_, _ = io.WriteString(w, `{"success":true,"result":{"status":"active"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"success":true,"result":{"id":"account"}}`)
	}))
	defer server.Close()
	adapter := newCloudflare(server.Client(), server.URL)
	report := adapter.Preflight(context.Background(), targetPlan("cloudflare", map[string]string{"accountId": "account"}), Credentials{CloudflareAPIToken: "cf-token"})
	if !report.Ready {
		t.Fatalf("unexpected Cloudflare preflight: %+v", report)
	}
}

func TestControlPlaneFailureDoesNotExposeBearerToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "rejected", http.StatusUnauthorized)
	}))
	defer server.Close()
	const secret = "do-not-return-this-token"
	report := newCloudflare(server.Client(), server.URL).Preflight(context.Background(), targetPlan("cloudflare", map[string]string{"accountId": "account"}), Credentials{CloudflareAPIToken: secret})
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) {
		t.Fatal("target report exposed bearer token")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestLocalDockerUsesEngineAPI(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body := "OK"
		contentType := "text/plain"
		if request.URL.Path == "/version" {
			encoded, _ := json.Marshal(map[string]string{"Version": "29.0.0", "ApiVersion": "1.52", "Os": "linux"})
			body, contentType = string(encoded), "application/json"
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	report := (&LocalDocker{client: client}).Preflight(context.Background(), targetPlan("local-compose", nil), Credentials{})
	if !report.Ready {
		t.Fatalf("unexpected Docker preflight: %+v", report)
	}
}

func TestPinnedSSHHostKeyRejectsMismatch(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, err := ssh.NewPublicKey(&privateKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := ssh.FingerprintSHA256(publicKey)
	if err := pinnedHostKey(fingerprint)("host", nil, publicKey); err != nil {
		t.Fatalf("matching host key rejected: %v", err)
	}
	if err := pinnedHostKey("SHA256:not-the-host")("host", nil, publicKey); err == nil {
		t.Fatal("mismatched host key accepted")
	}
}

func TestCredentialsClear(t *testing.T) {
	credentials := Credentials{AzureAccessToken: "one", CloudflareAPIToken: "two", SSHPrivateKey: "three", SSHPrivateKeyPassphrase: "four", SSHPassword: "five"}
	credentials.Clear()
	if credentials != (Credentials{}) {
		t.Fatalf("credentials were not cleared: %+v", credentials)
	}
}

func TestCredentialsRejectOversizedSecret(t *testing.T) {
	credentials := Credentials{SSHPrivateKey: strings.Repeat("x", (1<<20)+1)}
	if err := credentials.Validate(); err == nil {
		t.Fatal("oversized private key was accepted")
	}
}
