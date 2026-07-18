package adapter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestCompanionImageTrustRecordsSupportCurrentAndPriorRelease(t *testing.T) {
	current, err := companionForImage(repoWranglerV1012Companion.image)
	if err != nil || current.runtimeImage != "repo-wrangler-ranch-hand:v1.0.12" || current.imageID != "sha256:0882c997a463d41b3cd551208de805bd3fdfd5cb8cf3ea3a11ef96a088327215" {
		t.Fatalf("current RepoWrangler companion trust record is invalid: %#v %v", current, err)
	}
	prior, err := companionForImage(repoWranglerV1010Companion.image)
	if err != nil || prior.runtimeImage != repoWranglerV1010Companion.runtimeImage {
		t.Fatalf("prior RepoWrangler companion trust record was not retained: %#v %v", prior, err)
	}
	if _, err := companionForImage("ghcr.io/wranglerlabs/repo-wrangler-server@sha256:" + strings.Repeat("f", 64)); err == nil {
		t.Fatal("unknown RepoWrangler image unexpectedly received a companion trust record")
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
