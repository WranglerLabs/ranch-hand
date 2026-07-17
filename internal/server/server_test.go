package server

import (
	"context"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	productrelease "github.com/WranglerLabs/ranch-hand/internal/release"
)

func testUI() fs.FS { return fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}} }

type fakeReleaseVerifier struct {
	request productrelease.Request
}

func (f *fakeReleaseVerifier) VerifyAndCache(_ context.Context, request productrelease.Request) (productrelease.VerifiedArtifact, error) {
	f.request = request
	return productrelease.VerifiedArtifact{Product: "RepoWrangler", Version: request.Version, Target: request.Target, CachePath: `C:\cache\bundle.tar.gz`}, nil
}

func TestStatusRequiresLaunchToken(t *testing.T) {
	h := New("secret-token", "test", testUI())
	request := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("got %d", response.Code)
	}

	request = httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	response = httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("got %d: %s", response.Code, response.Body.String())
	}
}

func TestMutationRejectsCrossOrigin(t *testing.T) {
	h := New("secret-token", "test", testUI())
	request := httptest.NewRequest(http.MethodPost, "/api/v1/plans/validate", strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer secret-token")
	request.Header.Set("Origin", "https://attacker.example")
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("got %d", response.Code)
	}
}

func TestSecurityHeaders(t *testing.T) {
	h := New("secret-token", "test", testUI())
	response := httptest.NewRecorder()
	h.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("missing CSP")
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("missing no-store")
	}
}

func TestVerifyReleaseRequiresTokenAndSameOrigin(t *testing.T) {
	verifier := &fakeReleaseVerifier{}
	h := NewWithReleaseVerifier("secret-token", "test", testUI(), verifier)
	body := `{"manifestUrl":"https://github.com/WranglerLabs/repo-wrangler/releases/download/v1.2.3/release-manifest.json","version":"v1.2.3","target":"local-compose"}`

	request := httptest.NewRequest(http.MethodPost, "/api/v1/releases/verify", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer secret-token")
	request.Header.Set("Origin", "https://attacker.example")
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("cross-origin request returned %d", response.Code)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/releases/verify", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer secret-token")
	request.Header.Set("Origin", "http://example.com")
	request.Host = "example.com"
	response = httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("verification returned %d: %s", response.Code, response.Body.String())
	}
	if verifier.request.Version != "v1.2.3" || verifier.request.Target != "local-compose" {
		t.Fatalf("unexpected verifier request: %+v", verifier.request)
	}
}
