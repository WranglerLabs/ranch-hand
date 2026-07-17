package server

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	productrelease "github.com/WranglerLabs/ranch-hand/internal/release"
)

func testUI() fs.FS { return fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}} }

type fakeReleaseVerifier struct {
	request   productrelease.Request
	cachePath string
}

func (f *fakeReleaseVerifier) VerifyAndCache(_ context.Context, request productrelease.Request) (productrelease.VerifiedArtifact, error) {
	f.request = request
	return productrelease.VerifiedArtifact{
		Product: "RepoWrangler", Version: request.Version, Target: request.Target, CachePath: f.cachePath,
		ManifestURL: request.ManifestURL, ManifestSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		SHA256: "1e6ed65d77d6364eeaed5a745ba5c4985ae2b700dd85d7cf7f027bdf294a33fc", Size: 6,
		ProvenanceVerified: true, SBOMVerified: true,
	}, nil
}

func TestCreateExportPreflightAndDryRunVerifiedPlan(t *testing.T) {
	artifact := t.TempDir() + string(os.PathSeparator) + "bundle.tar.gz"
	if err := os.WriteFile(artifact, []byte("bundle"), 0o600); err != nil {
		t.Fatal(err)
	}
	verifier := &fakeReleaseVerifier{cachePath: artifact}
	h := NewWithReleaseVerifier("secret-token", "test", testUI(), verifier)
	manifest := "https://github.com/WranglerLabs/repo-wrangler/releases/download/v1.2.3/release-manifest.json"
	verifyBody := `{"manifestUrl":"` + manifest + `","version":"v1.2.3","target":"local-compose"}`
	response := authorizedPost(h, "/api/v1/releases/verify", verifyBody)
	if response.Code != http.StatusOK {
		t.Fatalf("verification returned %d: %s", response.Code, response.Body.String())
	}

	createBody := `{"name":"Local Wrangler","version":"v1.2.3","target":"local-compose","configuration":{"projectName":"repo-wrangler","dataDirectory":"C:\\\\RepoWrangler\\\\data","listenAddress":"127.0.0.1:8080"}}`
	response = authorizedPost(h, "/api/v1/plans/create", createBody)
	if response.Code != http.StatusCreated {
		t.Fatalf("plan creation returned %d: %s", response.Code, response.Body.String())
	}
	var created struct {
		Plan          json.RawMessage `json:"plan"`
		CanonicalJSON string          `json:"canonicalJson"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(created.CanonicalJSON, "\n") {
		t.Fatal("canonical plan must end with a newline")
	}

	response = authorizedPost(h, "/api/v1/plans/export", string(created.Plan))
	if response.Code != http.StatusOK || !strings.Contains(response.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("plan export returned %d: %s", response.Code, response.Body.String())
	}
	response = authorizedPost(h, "/api/v1/plans/dry-run", string(created.Plan))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"mutated":false`) {
		t.Fatalf("dry run returned %d: %s", response.Code, response.Body.String())
	}
	response = authorizedPost(h, "/api/v1/plans/preflight", string(created.Plan))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"ready":true`) {
		t.Fatalf("preflight returned %d: %s", response.Code, response.Body.String())
	}
}

func authorizedPost(h http.Handler, path, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer secret-token")
	request.Header.Set("Origin", "http://example.com")
	request.Host = "example.com"
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	return response
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
