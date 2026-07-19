package release

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var testSBOM = []byte(`{"spdxVersion":"SPDX-2.3","dataLicense":"CC0-1.0","SPDXID":"SPDXRef-DOCUMENT","name":"RepoWrangler","packages":[{"SPDXID":"SPDXRef-Package","name":"repo-wrangler"}]}`)

type fakeProvenanceVerifier struct {
	digests []string
	err     error
}

func (f *fakeProvenanceVerifier) Verify(_ []byte, digest string) error {
	f.digests = append(f.digests, digest)
	return f.err
}

func digest(data []byte) string {
	value := sha256.Sum256(data)
	return hex.EncodeToString(value[:])
}

func releaseServer(t *testing.T, artifact []byte, artifactDigest string) (*httptest.Server, *Service, Request) {
	t.Helper()
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1.2.3/release-manifest.json":
			manifest := Manifest{
				SchemaVersion: SchemaVersion,
				Product:       Product,
				Version:       "v1.2.3",
				ReleasedAt:    "2026-07-16T20:00:00Z",
				Artifacts: []Artifact{{
					Target: "local-compose", URL: server.URL + "/v1.2.3/bundle.tar.gz",
					SHA256: artifactDigest, Size: int64(len(artifact)), MediaType: "application/gzip",
					SBOMURL:        server.URL + "/v1.2.3/bundle.spdx.json",
					AttestationURL: server.URL + "/v1.2.3/bundle.provenance.sigstore.json",
				}},
			}
			_ = json.NewEncoder(response).Encode(manifest)
		case "/v1.2.3/bundle.tar.gz":
			_, _ = response.Write(artifact)
		case "/v1.2.3/bundle.spdx.json":
			_, _ = response.Write(testSBOM)
		case "/v1.2.3/bundle.provenance.sigstore.json":
			_, _ = response.Write([]byte(`{"verified-by":"fake test verifier"}`))
		default:
			http.NotFound(response, request)
		}
	}))
	t.Cleanup(server.Close)
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewServiceWithClient(t.TempDir(), server.Client(), []string{parsed.Hostname()}, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	service.provenance = &fakeProvenanceVerifier{}
	request := Request{ManifestURL: server.URL + "/v1.2.3/release-manifest.json", Version: "v1.2.3", Target: "local-compose"}
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
	if !verified.ProvenanceVerified || !verified.SBOMVerified {
		t.Fatal("supply-chain evidence was not classified as verified")
	}
	if len(service.provenance.(*fakeProvenanceVerifier).digests) != 2 {
		t.Fatal("artifact and SBOM provenance were not both verified")
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

func TestDiscoversNewestCompatibleStableRelease(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/releases":
			_ = json.NewEncoder(response).Encode([]map[string]any{
				{"tag_name": "v1.0.11-rc.1", "draft": false, "prerelease": true, "assets": []map[string]string{{"name": "release-manifest.json", "browser_download_url": server.URL + "/v1.0.11-rc.1/release-manifest.json"}}},
				{"tag_name": "v1.0.10", "draft": false, "prerelease": false, "assets": []map[string]string{{"name": "release-manifest.json", "browser_download_url": server.URL + "/v1.0.10/release-manifest.json"}}},
			})
		case "/v1.0.10/release-manifest.json":
			_ = json.NewEncoder(response).Encode(Manifest{SchemaVersion: SchemaVersion, Product: Product, Version: "v1.0.10", ReleasedAt: "2026-07-17T12:00:00Z", Artifacts: []Artifact{{
				Target: "local-compose", URL: server.URL + "/v1.0.10/bundle.tar.gz", SHA256: strings.Repeat("a", 64), Size: 42,
			}}})
		case "/v1.0.11-rc.1/release-manifest.json":
			_ = json.NewEncoder(response).Encode(Manifest{SchemaVersion: SchemaVersion, Product: Product, Version: "v1.0.11-rc.1", ReleasedAt: "2026-07-18T12:00:00Z", Artifacts: []Artifact{{
				Target: "local-compose", URL: server.URL + "/v1.0.11-rc.1/bundle.tar.gz", SHA256: strings.Repeat("b", 64), Size: 43,
			}}})
		default:
			http.NotFound(response, request)
		}
	}))
	t.Cleanup(server.Close)
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewServiceWithClient(t.TempDir(), server.Client(), []string{parsed.Hostname()}, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	discovered, err := service.Discover(context.Background(), "stable", "local-compose")
	if err != nil {
		t.Fatalf("discover stable release: %v", err)
	}
	if discovered.Version != "v1.0.10" || discovered.Prerelease || discovered.ManifestURL != server.URL+"/v1.0.10/release-manifest.json" {
		t.Fatalf("unexpected discovery result: %+v", discovered)
	}
	available, err := service.List(context.Background(), "local-compose")
	if err != nil {
		t.Fatalf("list releases: %v", err)
	}
	if len(available) != 2 || available[0].Version != "v1.0.11-rc.1" || !available[0].Prerelease || available[1].Version != "v1.0.10" || available[1].Prerelease {
		t.Fatalf("unexpected release catalog: %+v", available)
	}
}

func TestRejectsUnverifiedProvenance(t *testing.T) {
	contents := []byte("bundle")
	_, service, request := releaseServer(t, contents, digest(contents))
	service.provenance = &fakeProvenanceVerifier{err: errors.New("invalid signature")}
	if _, err := service.VerifyAndCache(context.Background(), request); err == nil || !strings.Contains(err.Error(), "artifact provenance") {
		t.Fatalf("expected provenance rejection, got %v", err)
	}
}

func TestRejectsIncompleteSPDX(t *testing.T) {
	if err := validateSPDX([]byte(`{"spdxVersion":"SPDX-2.3","name":"not enough"}`)); err == nil {
		t.Fatal("incomplete SPDX document was accepted")
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
	service, err := NewServiceWithClient(t.TempDir(), server.Client(), []string{parsed.Hostname()}, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.VerifyAndCache(context.Background(), Request{ManifestURL: server.URL + "/v1.2.3/release-manifest.json", Version: "v1.2.3", Target: "cloudflare"})
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
	service, err := NewServiceWithClient(t.TempDir(), server.Client(), []string{parsed.Hostname()}, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.VerifyAndCache(context.Background(), Request{ManifestURL: server.URL + "/v1.2.3/release-manifest.json", Version: "v1.2.3", Target: "cloudflare"})
	if err == nil || !strings.Contains(err.Error(), "untrusted release redirect") {
		t.Fatalf("expected redirect rejection, got %v", err)
	}
}

func TestRejectsNonCanonicalGitHubManifestPath(t *testing.T) {
	service, err := NewService(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.validateManifestURL("https://github.com/WranglerLabs/repo-wrangler/releases/latest/download/release-manifest.json", "v1.2.3")
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
