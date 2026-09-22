package adapter

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	productrelease "github.com/WranglerLabs/ranch-hand/internal/release"
)

const (
	maxPublishedImageArchive = int64(4 << 30)
	maxPublishedProvenance   = int64(8 << 20)
	maxArchiveMetadata       = int64(16 << 20)
)

var publishedRepoWranglerImage = regexp.MustCompile(`^ghcr\.io/wranglerlabs/repo-wrangler-server@sha256:[a-f0-9]{64}$`)
var archiveDigest = regexp.MustCompile(`^[a-f0-9]{64}$`)

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

var repoWranglerV1013Companion = companionImage{
	image:        "ghcr.io/wranglerlabs/repo-wrangler-server@sha256:2efa960d2dc199d23aaf92c226b8775b2ceab592ec8be2c853a6309e72d26f29",
	runtimeImage: "repo-wrangler-ranch-hand:v1.0.13",
	imageID:      "sha256:2b0aacea58a2d7cc078491d761d30b0d38b146ea0930e44147b4919ba248dfba",
	imageIDs: []string{
		"sha256:2efa960d2dc199d23aaf92c226b8775b2ceab592ec8be2c853a6309e72d26f29",
		"sha256:624ed1b87bdfc503a4a3031665fb813a71510fd56e11d289e623f0a17764053c",
		"sha256:2b0aacea58a2d7cc078491d761d30b0d38b146ea0930e44147b4919ba248dfba",
	},
	url:    "https://github.com/WranglerLabs/repo-wrangler/releases/download/v1.0.13/repo-wrangler-v1.0.13-linux-amd64-image.tar.gz",
	sha256: "71a82f8ffdb1bff13b98029dae8dd1c400fbe7a20ddedab3f48b381daf9476d6",
	size:   280755795,
}

var repoWranglerV1014Companion = companionImage{
	image:        "ghcr.io/wranglerlabs/repo-wrangler-server@sha256:d4a4ed70f919f85c8bd337885c0123fa7c100266616d10dbc9b9a17ceb57a8e9",
	runtimeImage: "repo-wrangler-ranch-hand:v1.0.14",
	imageID:      "sha256:1b9e11ffcdaf48e9481064bc7ab1ea93ebc4872f120624e3798b36c69f156b5e",
	imageIDs: []string{
		"sha256:d4a4ed70f919f85c8bd337885c0123fa7c100266616d10dbc9b9a17ceb57a8e9",
		"sha256:ef3b0886a895bf4ba58311af475ea9fee2a3a16f9d80a013cd313cc57ebfcee8",
		"sha256:1b9e11ffcdaf48e9481064bc7ab1ea93ebc4872f120624e3798b36c69f156b5e",
	},
	url:    "https://github.com/WranglerLabs/repo-wrangler/releases/download/v1.0.14/repo-wrangler-v1.0.14-linux-amd64-image.tar.gz",
	sha256: "444df865f825174cac14d470ad2500fbbd87b99951f7fd14d9c4cadc1361bd06",
	size:   280764825,
}

var repoWranglerV1015Companion = companionImage{
	image:        "ghcr.io/wranglerlabs/repo-wrangler-server@sha256:55a0ddd70e682eeb1becd6c608537a9f1d34ced7c4e3892806e84998f43412cc",
	runtimeImage: "repo-wrangler-ranch-hand:v1.0.15",
	imageID:      "sha256:ba2e58c2c3921bb18d67f56c13bad8f53e773f548b5d9ce71904b503b0184bac",
	imageIDs: []string{
		"sha256:55a0ddd70e682eeb1becd6c608537a9f1d34ced7c4e3892806e84998f43412cc",
		"sha256:1aa1514aef4719f9d5a22f5a4136224b51f113be8f2fac2f0671d5a8851ecd16",
		"sha256:ba2e58c2c3921bb18d67f56c13bad8f53e773f548b5d9ce71904b503b0184bac",
	},
	url:    "https://github.com/WranglerLabs/repo-wrangler/releases/download/v1.0.15/repo-wrangler-v1.0.15-linux-amd64-image.tar.gz",
	sha256: "75cb15cdcab12e007f6306d0553b108fc06e069303de48a62ad5cfe52a9c02a5",
	size:   280765036,
}

var repoWranglerV1016Companion = companionImage{
	image:        "ghcr.io/wranglerlabs/repo-wrangler-server@sha256:2db2198b4573d3c699edf416730c825b56f106356154c45f008c7745be9eba70",
	runtimeImage: "repo-wrangler-ranch-hand:v1.0.16",
	imageID:      "sha256:05ecc27552403fd358dc08f7e57fcbb73ca92c335cd31e8c40f35fd3bf62d759",
	imageIDs: []string{
		"sha256:2db2198b4573d3c699edf416730c825b56f106356154c45f008c7745be9eba70",
		"sha256:0c47c9f2fd048fdb0149228177af864eb0663bc50d6b7eaa0aeaf36afaf600ba",
		"sha256:05ecc27552403fd358dc08f7e57fcbb73ca92c335cd31e8c40f35fd3bf62d759",
	},
	url:    "https://github.com/WranglerLabs/repo-wrangler/releases/download/v1.0.16/repo-wrangler-v1.0.16-linux-amd64-image.tar.gz",
	sha256: "27074e93fec1ddca6ccf36fc6785aa7a5a04da4fba3375da534d094c5dd635ff",
	size:   280816012,
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
	if image == repoWranglerV1016Companion.image {
		return repoWranglerV1016Companion, nil
	}
	if image == repoWranglerV1015Companion.image {
		return repoWranglerV1015Companion, nil
	}
	if image == repoWranglerV1014Companion.image {
		return repoWranglerV1014Companion, nil
	}
	if image == repoWranglerV1013Companion.image {
		return repoWranglerV1013Companion, nil
	}
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

// resolveCompanionArchive retains the explicit trust records required by old
// releases, but new releases are no longer coupled to the Ranch Hand build.
// Their independently published image archive is accepted only after its
// digest verifies against the release's Sigstore provenance and its Docker
// archive identity matches the selected immutable version.
func resolveCompanionArchive(ctx context.Context, image, version, provenancePath string, client *http.Client, root string) (companionImage, string, error) {
	if companion, err := companionForImage(image); err == nil {
		archive, cacheErr := cacheCompanionImage(ctx, companion, client, root)
		return companion, archive, cacheErr
	}
	if productrelease.ValidateVersion(version) != nil || !publishedRepoWranglerImage.MatchString(image) {
		return companionImage{}, "", errors.New("release image identity is not a supported immutable RepoWrangler image")
	}
	provenance, err := readCompanionFile(provenancePath, maxPublishedProvenance)
	if err != nil {
		return companionImage{}, "", fmt.Errorf("read verified release provenance: %w", err)
	}
	filename := "repo-wrangler-" + version + "-linux-amd64-image.tar.gz"
	url := "https://github.com/WranglerLabs/repo-wrangler/releases/download/" + version + "/" + filename
	directory := filepath.Join(root, "images", "releases", version)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return companionImage{}, "", fmt.Errorf("create published image cache: %w", err)
	}
	destination := filepath.Join(directory, filename)
	verifier, err := productrelease.NewSigstoreProvenanceVerifier(root)
	if err != nil {
		return companionImage{}, "", err
	}
	if companion, err := verifyPublishedImageArchive(destination, image, version, url, provenance, verifier); err == nil {
		return companion, destination, nil
	}
	_ = os.Remove(destination)

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return companionImage{}, "", err
	}
	request.Header.Set("Accept", "application/octet-stream")
	response, err := trustedPublishedImageClient(client).Do(request)
	if err != nil {
		return companionImage{}, "", fmt.Errorf("download published release image archive: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return companionImage{}, "", fmt.Errorf("download published release image archive: release host returned HTTP %d", response.StatusCode)
	}
	temporary, err := os.CreateTemp(directory, ".published-image-*.partial")
	if err != nil {
		return companionImage{}, "", err
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	written, err := io.Copy(temporary, io.LimitReader(response.Body, maxPublishedImageArchive+1))
	if err != nil {
		return companionImage{}, "", fmt.Errorf("cache published release image archive: %w", err)
	}
	if written < 1 || written > maxPublishedImageArchive {
		return companionImage{}, "", errors.New("published release image archive exceeds the safety limit")
	}
	if err := temporary.Sync(); err != nil {
		return companionImage{}, "", err
	}
	if err := temporary.Close(); err != nil {
		return companionImage{}, "", err
	}
	companion, err := verifyPublishedImageArchive(temporaryPath, image, version, url, provenance, verifier)
	if err != nil {
		return companionImage{}, "", err
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		return companionImage{}, "", fmt.Errorf("commit published release image archive: %w", err)
	}
	committed = true
	return companion, destination, nil
}

func trustedPublishedImageClient(client *http.Client) *http.Client {
	copyClient := *client
	previous := client.CheckRedirect
	copyClient.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) >= 5 || request.URL.Scheme != "https" {
			return errors.New("untrusted published image redirect")
		}
		host := strings.ToLower(request.URL.Hostname())
		if host != "github.com" && host != "objects.githubusercontent.com" && host != "release-assets.githubusercontent.com" {
			return errors.New("untrusted published image redirect")
		}
		if previous != nil {
			return previous(request, via)
		}
		return nil
	}
	return &copyClient
}

func readCompanionFile(path string, maximum int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(contents)) > maximum {
		return nil, errors.New("file exceeds the safety limit")
	}
	return contents, nil
}

func verifyPublishedImageArchive(path, image, version, url string, provenance []byte, verifier productrelease.ProvenanceVerifier) (companionImage, error) {
	file, err := os.Open(path)
	if err != nil {
		return companionImage{}, err
	}
	info, err := file.Stat()
	if err != nil || info.Size() < 1 || info.Size() > maxPublishedImageArchive {
		_ = file.Close()
		return companionImage{}, errors.New("published image archive size is invalid")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		_ = file.Close()
		return companionImage{}, err
	}
	_ = file.Close()
	digest := hex.EncodeToString(hash.Sum(nil))
	if err := verifier.Verify(provenance, digest); err != nil {
		return companionImage{}, fmt.Errorf("verify published image archive provenance: %w", err)
	}
	runtimeImage, imageIDs, err := inspectPublishedImageArchive(path, version)
	if err != nil {
		return companionImage{}, err
	}
	return companionImage{image: image, runtimeImage: runtimeImage, imageID: imageIDs[0], imageIDs: imageIDs, url: url, sha256: digest, size: info.Size()}, nil
}

func inspectPublishedImageArchive(archivePath, version string) (string, []string, error) {
	file, err := os.Open(archivePath)
	if err != nil {
		return "", nil, err
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return "", nil, fmt.Errorf("open published image gzip: %w", err)
	}
	defer gzipReader.Close()
	tarReader := tar.NewReader(gzipReader)
	metadata := make(map[string][]byte)
	var metadataSize int64
	for {
		header, nextErr := tarReader.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return "", nil, fmt.Errorf("read published image archive: %w", nextErr)
		}
		if header.Typeflag != tar.TypeReg || (header.Name != "manifest.json" && !strings.HasPrefix(filepath.ToSlash(header.Name), "blobs/sha256/")) || header.Size < 1 || header.Size > 4<<20 {
			continue
		}
		metadataSize += header.Size
		if metadataSize > maxArchiveMetadata {
			return "", nil, errors.New("published image archive metadata exceeds the safety limit")
		}
		contents, readErr := io.ReadAll(io.LimitReader(tarReader, header.Size))
		if readErr != nil || int64(len(contents)) != header.Size {
			return "", nil, errors.New("published image archive metadata is truncated")
		}
		metadata[filepath.ToSlash(header.Name)] = contents
	}
	var manifest []struct {
		Config   string   `json:"Config"`
		RepoTags []string `json:"RepoTags"`
	}
	if json.Unmarshal(metadata["manifest.json"], &manifest) != nil || len(manifest) != 1 || len(manifest[0].RepoTags) != 1 {
		return "", nil, errors.New("published image archive manifest is invalid")
	}
	runtimeImage := "repo-wrangler-ranch-hand:" + version
	if manifest[0].RepoTags[0] != runtimeImage {
		return "", nil, errors.New("published image archive tag does not match the selected release")
	}
	configPath := filepath.ToSlash(manifest[0].Config)
	configDigest := filepath.Base(configPath)
	if !archiveDigest.MatchString(configDigest) {
		return "", nil, errors.New("published image archive config identity is invalid")
	}
	config, ok := metadata[configPath]
	if !ok {
		return "", nil, errors.New("published image archive config is missing")
	}
	actualConfigDigest := sha256.Sum256(config)
	if hex.EncodeToString(actualConfigDigest[:]) != configDigest {
		return "", nil, errors.New("published image archive config digest does not match its content")
	}
	imageIDs := []string{"sha256:" + configDigest}
	for path, contents := range metadata {
		blobDigest := filepath.Base(path)
		if path == configPath || !strings.HasPrefix(path, "blobs/sha256/") || !archiveDigest.MatchString(blobDigest) {
			continue
		}
		actualBlobDigest := sha256.Sum256(contents)
		if hex.EncodeToString(actualBlobDigest[:]) != blobDigest {
			continue
		}
		var candidate struct {
			SchemaVersion int `json:"schemaVersion"`
			Config        struct {
				Digest string `json:"digest"`
			} `json:"config"`
		}
		if json.Unmarshal(contents, &candidate) == nil && candidate.SchemaVersion == 2 && candidate.Config.Digest == "sha256:"+configDigest {
			imageIDs = append(imageIDs, "sha256:"+blobDigest)
		}
	}
	return runtimeImage, imageIDs, nil
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

func prepareWSLCompanion(ctx context.Context, distribution, image, version, provenancePath string) (string, error) {
	root, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("locate Ranch Hand image cache: %w", err)
	}
	client := &http.Client{Timeout: 30 * time.Minute}
	companion, archive, err := resolveCompanionArchive(ctx, image, version, provenancePath, client, filepath.Join(root, "WranglerLabs", "Ranch Hand"))
	if err != nil {
		return "", err
	}
	if err := loadWSLImageArchive(ctx, distribution, archive, companion); err != nil {
		return "", err
	}
	return companion.runtimeImage, nil
}
