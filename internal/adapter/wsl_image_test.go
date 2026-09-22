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
	current, err := companionForImage(repoWranglerV1015Companion.image)
	if err != nil || current.runtimeImage != "repo-wrangler-ranch-hand:v1.0.15" || len(current.imageIDs) != 3 {
		t.Fatalf("current RepoWrangler companion trust record is invalid: %#v %v", current, err)
	}
	for _, prior := range []companionImage{repoWranglerV1014Companion, repoWranglerV1013Companion, repoWranglerV1012Companion, repoWranglerV1010Companion} {
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
	for _, companion := range []companionImage{repoWranglerV1010Companion, repoWranglerV1012Companion, repoWranglerV1013Companion, repoWranglerV1014Companion, repoWranglerV1015Companion} {
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
