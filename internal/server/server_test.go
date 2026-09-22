package server

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func testUI() fs.FS { return fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}} }

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
