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
		if strings.Contains(strings.ToLower(r.URL.Path), "/resourcegroups/") {
			http.Error(w, "missing", http.StatusNotFound)
			return
		}
		_, _ = io.WriteString(w, `{"id":"subscription","state":"Enabled"}`)
	}))
	defer server.Close()
	adapter := newAzureContainerApps(server.Client(), server.URL)
	report := adapter.Preflight(context.Background(), targetPlan("azure-container-apps", map[string]string{
		"subscriptionId": "00000000-0000-0000-0000-000000000000", "resourceGroup": "rg-ranch-hand", "location": "eastus",
		"environmentName": "cae-ranch-hand", "appName": "repo-wrangler",
	}), Credentials{AzureAccessToken: "azure-token"})
	if !report.Ready || requests != 3 {
		t.Fatalf("unexpected ARM preflight: %+v; requests=%d", report, requests)
	}
}

func TestCloudflareVerifiesTokenAndAccount(t *testing.T) {
	accountID := "0123456789abcdef0123456789abcdef"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer cf-token" {
			t.Fatal("missing Cloudflare bearer token")
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/user/tokens/verify") {
			_, _ = io.WriteString(w, `{"success":true,"result":{"status":"active"}}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/workers/subdomain") {
			_, _ = io.WriteString(w, `{"success":true,"result":{"subdomain":"wranglerlabs"}}`)
			return
		}
		if strings.Contains(r.URL.Path, "/workers/scripts/") {
			http.Error(w, "missing", http.StatusNotFound)
			return
		}
		if strings.Contains(r.URL.Path, "/d1/database") {
			_, _ = io.WriteString(w, `{"success":true,"result":[]}`)
			return
		}
		_, _ = io.WriteString(w, `{"success":true,"result":{"id":"`+accountID+`"}}`)
	}))
	defer server.Close()
	adapter := newCloudflare(server.Client(), server.URL)
	report := adapter.Preflight(context.Background(), targetPlan("cloudflare", map[string]string{"accountId": accountID, "workerName": "repo-wrangler", "databaseName": "repo-wrangler"}), Credentials{CloudflareAPIToken: "cf-token"})
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
	report := newCloudflare(server.Client(), server.URL).Preflight(context.Background(), targetPlan("cloudflare", map[string]string{"accountId": "0123456789abcdef0123456789abcdef", "workerName": "repo-wrangler", "databaseName": "repo-wrangler"}), Credentials{CloudflareAPIToken: secret})
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
	credentials := Credentials{AzureAccessToken: "one", CloudflareAPIToken: "two", SSHPrivateKey: "three", SSHPrivateKeyPassphrase: "four", SSHPassword: "five", SudoPassword: "six"}
	credentials.Clear()
	if credentials != (Credentials{}) {
		t.Fatalf("credentials were not cleared: %+v", credentials)
	}
}

func TestDockerPrerequisiteScriptIsBoundedAndQuotesUser(t *testing.T) {
	script := dockerPrerequisiteScript("administrator")
	for _, required := range []string{"/etc/os-release", "apt-get install --yes docker.io", "docker compose version", "usermod -aG docker 'administrator'"} {
		if !strings.Contains(script, required) {
			t.Fatalf("prerequisite script is missing %q", required)
		}
	}
	if remoteUserPatternForPrerequisites("admin; reboot") {
		t.Fatal("unsafe prerequisite user was accepted")
	}
}

func TestCredentialsRejectOversizedSecret(t *testing.T) {
	credentials := Credentials{SSHPrivateKey: strings.Repeat("x", (1<<20)+1)}
	if err := credentials.Validate(); err == nil {
		t.Fatal("oversized private key was accepted")
	}
}
