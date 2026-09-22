package adapter

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

type recordingArchiveProvenance struct {
	digest string
	err    error
}

func (v *recordingArchiveProvenance) Verify(_ []byte, digest string) error {
	v.digest = digest
	return v.err
}

func TestCompanionImageTrustRecordsSupportCurrentAndPriorRelease(t *testing.T) {
	current, err := companionForImage(repoWranglerV1016Companion.image)
	if err != nil || current.runtimeImage != "repo-wrangler-ranch-hand:v1.0.16" || len(current.imageIDs) != 3 {
		t.Fatalf("current RepoWrangler companion trust record is invalid: %#v %v", current, err)
	}
	for _, prior := range []companionImage{repoWranglerV1015Companion, repoWranglerV1014Companion, repoWranglerV1013Companion, repoWranglerV1012Companion, repoWranglerV1010Companion} {
		resolved, err := companionForImage(prior.image)
		if err != nil || resolved.runtimeImage != prior.runtimeImage {
			t.Fatalf("prior RepoWrangler companion trust record was not retained: %#v %v", resolved, err)
		}
	}
	if _, err := companionForImage("ghcr.io/wranglerlabs/repo-wrangler-server@sha256:" + strings.Repeat("f", 64)); err == nil {
		t.Fatal("unknown RepoWrangler image unexpectedly received a companion trust record")
	}
}

func TestCompanionLoadedImageAcceptsOnlyVerifiedEngineIdentities(t *testing.T) {
	for _, companion := range []companionImage{repoWranglerV1010Companion, repoWranglerV1012Companion, repoWranglerV1013Companion, repoWranglerV1014Companion, repoWranglerV1015Companion, repoWranglerV1016Companion} {
		for _, identity := range companion.imageIDs {
			if !companionLoadedImageMatches(companion, identity+"\n") {
				t.Fatalf("verified image identity was rejected: %s", identity)
			}
		}
		if companionLoadedImageMatches(companion, "sha256:"+strings.Repeat("f", 64)) {
			t.Fatalf("unknown image identity was accepted for %s", companion.runtimeImage)
		}
	}
}

func TestCompanionImageDownloadVerifiesAndReusesCache(t *testing.T) {
	contents := []byte("verified image archive")
	digest := sha256.Sum256(contents)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		requests++
		_, _ = response.Write(contents)
	}))
	defer server.Close()
	companion := companionImage{url: server.URL + "/image.tar.gz", sha256: hex.EncodeToString(digest[:]), size: int64(len(contents))}
	first, err := cacheCompanionImage(context.Background(), companion, server.Client(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	second, err := cacheCompanionImage(context.Background(), companion, server.Client(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("test used different cache roots but produced the same path")
	}
	root := t.TempDir()
	first, err = cacheCompanionImage(context.Background(), companion, server.Client(), root)
	if err != nil {
		t.Fatal(err)
	}
	second, err = cacheCompanionImage(context.Background(), companion, server.Client(), root)
	if err != nil || first != second {
		t.Fatalf("verified cache was not reused: %q %q %v", first, second, err)
	}
	if requests != 3 {
		t.Fatalf("expected three network downloads and one cache reuse, got %d requests", requests)
	}
	if cached, err := os.ReadFile(second); err != nil || string(cached) != string(contents) {
		t.Fatal("cached companion bytes differ")
	}
}

func TestCompanionImageDownloadRejectsDigestMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte("tampered"))
	}))
	defer server.Close()
	companion := companionImage{url: server.URL + "/image.tar.gz", sha256: "0000000000000000000000000000000000000000000000000000000000000000", size: 8}
	if _, err := cacheCompanionImage(context.Background(), companion, server.Client(), t.TempDir()); err == nil {
		t.Fatal("tampered companion image was accepted")
	}
}

func TestPublishedCompanionDerivesIdentityFromProvenanceVerifiedArchive(t *testing.T) {
	version := "v1.0.17"
	archive := publishedImageArchive(t, version)
	verifier := &recordingArchiveProvenance{}
	image := "ghcr.io/wranglerlabs/repo-wrangler-server@sha256:" + strings.Repeat("a", 64)
	companion, err := verifyPublishedImageArchive(archive, image, version, "https://example.invalid/image.tar.gz", []byte("provenance"), verifier)
	if err != nil {
		t.Fatal(err)
	}
	if companion.runtimeImage != "repo-wrangler-ranch-hand:v1.0.17" || companion.image != image || !strings.HasPrefix(companion.imageID, "sha256:") || len(companion.imageIDs) != 2 || verifier.digest != companion.sha256 {
		t.Fatalf("unexpected dynamically verified companion: %#v digest=%s", companion, verifier.digest)
	}
	rejected := &recordingArchiveProvenance{err: errors.New("untrusted")}
	if _, err := verifyPublishedImageArchive(archive, image, version, "https://example.invalid/image.tar.gz", []byte("provenance"), rejected); err == nil {
		t.Fatal("archive rejected by provenance was accepted")
	}
}

func TestDynamicLoadedRuntimeImageMatchesSelectedRelease(t *testing.T) {
	image := "ghcr.io/wranglerlabs/repo-wrangler-server@sha256:" + strings.Repeat("a", 64)
	if !validLoadedRuntimeImage(image, "repo-wrangler-ranch-hand:v1.0.17", "v1.0.17") {
		t.Fatal("verified future release runtime image was rejected")
	}
	if validLoadedRuntimeImage(image, "repo-wrangler-ranch-hand:v1.0.18", "v1.0.17") {
		t.Fatal("runtime image from a different release was accepted")
	}
}

func publishedImageArchive(t *testing.T, version string) string {
	t.Helper()
	path := t.TempDir() + "/image.tar.gz"
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	config := []byte(`{"architecture":"amd64","os":"linux"}`)
	configHash := sha256.Sum256(config)
	configPath := "blobs/sha256/" + hex.EncodeToString(configHash[:])
	ociManifest, _ := json.Marshal(map[string]any{"schemaVersion": 2, "config": map[string]any{"digest": "sha256:" + hex.EncodeToString(configHash[:])}})
	manifestHash := sha256.Sum256(ociManifest)
	manifestPath := "blobs/sha256/" + hex.EncodeToString(manifestHash[:])
	manifest, _ := json.Marshal([]map[string]any{{"Config": configPath, "RepoTags": []string{"repo-wrangler-ranch-hand:" + version}, "Layers": []string{}}})
	for name, contents := range map[string][]byte{configPath: config, manifestPath: ociManifest, "manifest.json": manifest} {
		if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(contents))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write(contents); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}
