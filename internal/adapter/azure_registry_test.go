package adapter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/WranglerLabs/ranch-hand/internal/lifecycle"
)

func TestVerifyPublicGHCRImageRequiresExactAnonymousManifest(t *testing.T) {
	manifest := []byte(`{"schemaVersion":2,"manifests":[]}`)
	digest := sha256.Sum256(manifest)
	image := "ghcr.io/wranglerlabs/repo-wrangler-server@sha256:" + hex.EncodeToString(digest[:])
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/token":
			if request.URL.Query().Get("scope") != "repository:wranglerlabs/repo-wrangler-server:pull" {
				t.Fatal("anonymous token request used the wrong repository scope")
			}
			return registryResponse(http.StatusOK, `{"token":"anonymous-token"}`), nil
		case "/v2/wranglerlabs/repo-wrangler-server/manifests/" + strings.TrimPrefix(image, "ghcr.io/wranglerlabs/repo-wrangler-server@"):
			if request.Header.Get("Authorization") != "Bearer anonymous-token" {
				t.Fatal("manifest request omitted anonymous pull token")
			}
			return registryResponse(http.StatusOK, string(manifest)), nil
		default:
			t.Fatalf("unexpected registry request: %s", request.URL.String())
			return nil, nil
		}
	})}
	if err := verifyPublicGHCRImage(context.Background(), client, image); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyPublicGHCRImageRejectsPrivatePackage(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return registryResponse(http.StatusUnauthorized, `{"errors":[{"code":"UNAUTHORIZED"}]}`), nil
	})}
	image := "ghcr.io/wranglerlabs/repo-wrangler-server@sha256:" + strings.Repeat("a", 64)
	if err := verifyPublicGHCRImage(context.Background(), client, image); err == nil || !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("private package was not rejected: %v", err)
	}
}

func TestAzureStagedPreflightBlocksPrivateImageBeforeMutation(t *testing.T) {
	var writes int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writes++
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
	adapter.verifyPublicImage = func(context.Context, string) error { return io.ErrUnexpectedEOF }
	report := adapter.PreflightStaged(context.Background(), targetPlan("azure-container-apps", map[string]string{
		"subscriptionId": "00000000-0000-0000-0000-000000000000", "resourceGroup": "rg-ranch-hand", "location": "eastus",
		"environmentName": "cae-ranch-hand", "appName": "repo-wrangler",
	}), stagedAzureBundle(t), Credentials{AzureAccessToken: "azure-token"})
	if report.Ready || writes != 0 {
		t.Fatalf("private image was not blocked before mutation: report=%+v writes=%d", report, writes)
	}
}

func TestAzureApplyRechecksPublicImageBeforeMutation(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	adapter := newAzureContainerApps(server.Client(), server.URL)
	adapter.verifyPublicImage = func(context.Context, string) error { return io.ErrUnexpectedEOF }
	err := adapter.Apply(context.Background(), lifecycle.Install, azureEvaluationPlan(), "", stagedAzureBundle(t), lifecycle.OperationBackups{}, Credentials{AzureAccessToken: "azure-token"})
	if err == nil || requests != 0 {
		t.Fatalf("Azure apply did not stop before mutation: err=%v requests=%d", err, requests)
	}
}

func registryResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}
