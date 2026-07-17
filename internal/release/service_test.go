package release

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func digest(data []byte) string {
	value := sha256.Sum256(data)
	return hex.EncodeToString(value[:])
}

func releaseServer(t *testing.T, artifact []byte, artifactDigest string) (*httptest.Server, *Service, Request) {
	t.Helper()
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/release-manifest.json":
			manifest := Manifest{
				SchemaVersion: SchemaVersion,
				Product:       Product,
				Version:       "v1.2.3",
				ReleasedAt:    "2026-07-16T20:00:00Z",
				Artifacts: []Artifact{{
					Target: "local-compose", URL: server.URL + "/bundle.tar.gz",
					SHA256: artifactDigest, Size: int64(len(artifact)), MediaType: "application/gzip",
					SBOMURL: server.URL + "/bundle.spdx.json",
				}},
			}
			_ = json.NewEncoder(response).Encode(manifest)
		case "/bundle.tar.gz":
			_, _ = response.Write(artifact)
		default:
			http.NotFound(response, request)
		}
	}))
	t.Cleanup(server.Close)
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewServiceWithClient(t.TempDir(), server.Client(), []string{parsed.Hostname()})
	if err != nil {
		t.Fatal(err)
	}
	request := Request{ManifestURL: server.URL + "/release-manifest.json", Version: "v1.2.3", Target: "local-compose"}
	return server, service, request
}

func TestVerifyAndCacheArtifact(t *testing.T) {
	contents := []byte("immutable release bundle")
	_, service, request := releaseServer(t, contents, digest(contents))

	verified, err := service.VerifyAndCache(context.Background(), request)
	if err != nil {
		t.Fatalf("verify release: %v", err)
	}
	if verified.CacheHit {
		t.Fatal("first download unexpectedly reported a cache hit")
	}
	cached, err := os.ReadFile(verified.CachePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(cached) != string(contents) {
		t.Fatal("cached artifact bytes differ")
	}

	second, err := service.VerifyAndCache(context.Background(), request)
	if err != nil {
		t.Fatalf("verify cached release: %v", err)
	}
	if !second.CacheHit || second.CachePath != verified.CachePath {
		t.Fatal("second verification did not reuse the verified cache entry")
	}
}

func TestRejectsArtifactHashMismatchAndRemovesPartialFile(t *testing.T) {
	_, service, request := releaseServer(t, []byte("changed bytes"), strings.Repeat("a", 64))
	if _, err := service.VerifyAndCache(context.Background(), request); err == nil || !strings.Contains(err.Error(), "SHA-256 mismatch") {
		t.Fatalf("expected SHA-256 mismatch, got %v", err)
	}
	entries := 0
	_ = filepath.Walk(service.cacheRoot, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			entries++
		}
		return nil
	})
	if entries != 0 {
		t.Fatalf("partial artifact remained in cache: %d file(s)", entries)
	}
}

func TestRejectsManifestDigestMismatch(t *testing.T) {
	contents := []byte("bundle")
	_, service, request := releaseServer(t, contents, digest(contents))
	request.ManifestSHA256 = strings.Repeat("f", 64)
	if _, err := service.VerifyAndCache(context.Background(), request); err == nil || !strings.Contains(err.Error(), "manifest SHA-256") {
		t.Fatalf("expected manifest digest mismatch, got %v", err)
	}
}

func TestRejectsOversizedManifest(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write(make([]byte, maxManifestSize+1))
	}))
	defer server.Close()
	parsed, _ := url.Parse(server.URL)
	service, err := NewServiceWithClient(t.TempDir(), server.Client(), []string{parsed.Hostname()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.VerifyAndCache(context.Background(), Request{ManifestURL: server.URL, Version: "v1.2.3", Target: "cloudflare"})
	if err == nil || !strings.Contains(err.Error(), "safety limit") {
		t.Fatalf("expected manifest size failure, got %v", err)
	}
}

func TestRejectsRedirectToUntrustedHost(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		http.Redirect(response, request, "https://attacker.example/release-manifest.json", http.StatusFound)
	}))
	defer server.Close()
	parsed, _ := url.Parse(server.URL)
	service, err := NewServiceWithClient(t.TempDir(), server.Client(), []string{parsed.Hostname()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.VerifyAndCache(context.Background(), Request{ManifestURL: server.URL, Version: "v1.2.3", Target: "cloudflare"})
	if err == nil || !strings.Contains(err.Error(), "untrusted release redirect") {
		t.Fatalf("expected redirect rejection, got %v", err)
	}
}

func TestRejectsNonCanonicalGitHubManifestPath(t *testing.T) {
	service, err := NewService(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	err = service.validateManifestURL("https://github.com/WranglerLabs/repo-wrangler/releases/latest/download/release-manifest.json", "v1.2.3")
	if err == nil || !strings.Contains(err.Error(), "official versioned") {
		t.Fatalf("expected canonical path failure, got %v", err)
	}
}

func TestUnsafeArtifactFilenameCannotEscapeCacheDirectory(t *testing.T) {
	// A URL path ending in an encoded dot segment must never become a cache filename.
	filename := artifactFilename("https://example.test/%2e%2e", "local-compose")
	if filename != "local-compose.bundle" {
		t.Fatalf("unsafe URL produced cache filename %q", filename)
	}
}
