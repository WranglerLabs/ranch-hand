package adapter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type companionImage struct {
	image        string
	runtimeImage string
	imageID      string
	imageIDs     []string
	url          string
	sha256       string
	size         int64
}

var repoWranglerV1010Companion = companionImage{
	image:        "ghcr.io/wranglerlabs/repo-wrangler-server@sha256:89d1b4091137eef57c91270d363fb6c76e6d60c94dcac92b129b2b8629f45093",
	runtimeImage: "repo-wrangler-server:v1.0.10-ranch-hand",
	imageID:      "sha256:89d1b4091137eef57c91270d363fb6c76e6d60c94dcac92b129b2b8629f45093",
	imageIDs: []string{
		"sha256:89d1b4091137eef57c91270d363fb6c76e6d60c94dcac92b129b2b8629f45093",
		"sha256:380b6b16376f80cfca0fa7a989d5ad6b6eec93ed280f08ceaedad32078b04cdf",
		"sha256:b38ecd852041ddbc02749a5d5d0362d12aa2b8dd42d4b330499e31069525b18c",
	},
	url:    "https://github.com/WranglerLabs/ranch-hand/releases/download/v0.1.0-rc.13/repo-wrangler-v1.0.10-linux-amd64-image.tar.gz",
	sha256: "bc2c7507b592a6da58ec1eeed199d2c3b028bdb6a6b73f22a00ff7aab46ada5e",
	size:   286575554,
}

var repoWranglerV1012Companion = companionImage{
	image:        "ghcr.io/wranglerlabs/repo-wrangler-server@sha256:e4006a552ec2ece536bc737f6595bdbf8dc32d99f29c888fe9d06d5e09acffd7",
	runtimeImage: "repo-wrangler-ranch-hand:v1.0.12",
	imageID:      "sha256:0882c997a463d41b3cd551208de805bd3fdfd5cb8cf3ea3a11ef96a088327215",
	imageIDs: []string{
		"sha256:e4006a552ec2ece536bc737f6595bdbf8dc32d99f29c888fe9d06d5e09acffd7",
		"sha256:ddbed2c10d55733f40211cd2b7e2597839d878c9a97bbd59af68168fee656895",
		"sha256:0882c997a463d41b3cd551208de805bd3fdfd5cb8cf3ea3a11ef96a088327215",
	},
	url:    "https://github.com/WranglerLabs/repo-wrangler/releases/download/v1.0.12/repo-wrangler-v1.0.12-linux-amd64-image.tar.gz",
	sha256: "213773b172305e9ea6c8c700034b02f991632973908ac1524bb84a23d4ac870c",
	size:   280769404,
}

func companionLoadedImageMatches(companion companionImage, loaded string) bool {
	loaded = strings.TrimSpace(loaded)
	if loaded == companion.imageID {
		return true
	}
	for _, expected := range companion.imageIDs {
		if loaded == expected {
			return true
		}
	}
	return false
}

func companionForImage(image string) (companionImage, error) {
	if image == repoWranglerV1012Companion.image {
		return repoWranglerV1012Companion, nil
	}
	if image == repoWranglerV1010Companion.image {
		return repoWranglerV1010Companion, nil
	}
	return companionImage{}, fmt.Errorf("RepoWrangler release image %s has no verified public image archive", image)
}

func cacheCompanionImage(ctx context.Context, companion companionImage, client *http.Client, root string) (string, error) {
	if len(companion.sha256) != 64 || companion.size <= 0 {
		return "", errors.New("invalid companion image trust record")
	}
	directory := filepath.Join(root, "images", companion.sha256)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", fmt.Errorf("create image cache: %w", err)
	}
	destination := filepath.Join(directory, filepath.Base(companion.url))
	if matchesFile(destination, companion.sha256, companion.size) {
		return destination, nil
	}
	// Remove only an entry that failed verification before downloading. Never
	// remove the destination after downloading because another Ranch Hand process
	// may have committed the same verified archive in the meantime.
	_ = os.Remove(destination)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, companion.url, nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Accept", "application/octet-stream")
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("download verified public image archive: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download verified public image archive: release host returned HTTP %d", response.StatusCode)
	}
	temporary, err := os.CreateTemp(directory, ".image-*.partial")
	if err != nil {
		return "", err
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(temporary, hash), io.LimitReader(response.Body, companion.size+1))
	if copyErr != nil {
		return "", fmt.Errorf("cache verified public image archive: %w", copyErr)
	}
	if written != companion.size {
		return "", fmt.Errorf("image archive size mismatch: expected %d bytes, received %d", companion.size, written)
	}
	if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), companion.sha256) {
		return "", errors.New("image archive SHA-256 mismatch")
	}
	if err := temporary.Sync(); err != nil {
		return "", err
	}
	if err := temporary.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		if matchesFile(destination, companion.sha256, companion.size) {
			return destination, nil
		}
		return "", fmt.Errorf("commit verified image archive: %w", err)
	}
	committed = true
	return destination, nil
}

func matchesFile(path, expectedHash string, expectedSize int64) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Size() != expectedSize {
		return false
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return false
	}
	return strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), expectedHash)
}

func prepareWSLCompanion(ctx context.Context, distribution, image string) (string, error) {
	companion, err := companionForImage(image)
	if err != nil {
		return "", err
	}
	root, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("locate Ranch Hand image cache: %w", err)
	}
	client := &http.Client{Timeout: 30 * time.Minute}
	archive, err := cacheCompanionImage(ctx, companion, client, filepath.Join(root, "WranglerLabs", "Ranch Hand"))
	if err != nil {
		return "", err
	}
	if err := loadWSLImageArchive(ctx, distribution, archive, companion); err != nil {
		return "", err
	}
	return companion.runtimeImage, nil
}
