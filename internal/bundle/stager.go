package bundle

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	productrelease "github.com/WranglerLabs/ranch-hand/internal/release"
)

const (
	maximumFiles        = 10_000
	maximumExpandedSize = int64(512 << 20)
	maximumFileSize     = int64(256 << 20)
	stageManifestName   = ".ranch-hand-stage.json"
)

var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

type StagedBundle struct {
	Product  string `json:"product"`
	Version  string `json:"version"`
	Target   string `json:"target"`
	Path     string `json:"path"`
	CacheHit bool   `json:"cacheHit"`
}

type stagedFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type stageManifest struct {
	SchemaVersion  string       `json:"schemaVersion"`
	Product        string       `json:"product"`
	Version        string       `json:"version"`
	Target         string       `json:"target"`
	ArtifactSHA256 string       `json:"artifactSha256"`
	Files          []stagedFile `json:"files"`
}

type Identity struct {
	SchemaVersion          string `json:"schemaVersion"`
	Product                string `json:"product"`
	Version                string `json:"version"`
	TargetFamily           string `json:"targetFamily"`
	Image                  string `json:"image,omitempty"`
	PostgresImage          string `json:"postgresImage,omitempty"`
	PublicHTTPS            string `json:"publicHttps"`
	DefaultBindAddress     string `json:"defaultBindAddress,omitempty"`
	RegistryAuthentication string `json:"registryAuthentication,omitempty"`
	Worker                 string `json:"worker,omitempty"`
	AssetsDirectory        string `json:"assetsDirectory,omitempty"`
	MigrationsDirectory    string `json:"migrationsDirectory,omitempty"`
	CompatibilityDate      string `json:"compatibilityDate,omitempty"`
}

type Stager struct {
	root string
	mu   sync.Mutex
}

func NewStager(root string) (*Stager, error) {
	if root == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			return nil, fmt.Errorf("locate user cache: %w", err)
		}
		root = filepath.Join(base, "WranglerLabs", "Ranch Hand", "staged")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve staging root: %w", err)
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return nil, fmt.Errorf("create staging root: %w", err)
	}
	physical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, fmt.Errorf("resolve physical staging root: %w", err)
	}
	return &Stager{root: filepath.Clean(physical)}, nil
}

func (s *Stager) Stage(verified productrelease.VerifiedArtifact) (StagedBundle, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := validateVerifiedArtifact(verified); err != nil {
		return StagedBundle{}, err
	}
	family := targetFamily(verified.Target)
	destination := filepath.Join(s.root, verified.Version, strings.ToLower(verified.SHA256), family)
	if err := ensureWithin(s.root, destination); err != nil {
		return StagedBundle{}, err
	}
	if err := rejectSymlinkComponents(s.root, destination); err != nil {
		return StagedBundle{}, err
	}
	expectedFiles, err := extractArchive(verified.CachePath, "", family)
	if err != nil {
		return StagedBundle{}, err
	}
	if err := verifyStaged(destination, verified, expectedFiles); err == nil {
		return stagedResult(verified, destination, true), nil
	}
	if err := os.RemoveAll(destination); err != nil {
		return StagedBundle{}, fmt.Errorf("remove invalid staged bundle: %w", err)
	}
	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return StagedBundle{}, fmt.Errorf("create staging directory: %w", err)
	}
	temporary, err := os.MkdirTemp(parent, ".stage-*")
	if err != nil {
		return StagedBundle{}, fmt.Errorf("create temporary staging directory: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(temporary)
		}
	}()
	files, err := extractArchive(verified.CachePath, temporary, family)
	if err != nil {
		return StagedBundle{}, err
	}
	extractedRoot := filepath.Join(temporary, family)
	if err := validateBundleIdentity(extractedRoot, verified, family); err != nil {
		return StagedBundle{}, err
	}
	manifest := stageManifest{SchemaVersion: "1.0", Product: productrelease.Product, Version: verified.Version, Target: verified.Target, ArtifactSHA256: strings.ToLower(verified.SHA256), Files: files}
	manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return StagedBundle{}, err
	}
	if err := os.WriteFile(filepath.Join(extractedRoot, stageManifestName), append(manifestJSON, '\n'), 0o600); err != nil {
		return StagedBundle{}, fmt.Errorf("write stage manifest: %w", err)
	}
	if err := os.Rename(extractedRoot, destination); err != nil {
		return StagedBundle{}, fmt.Errorf("commit staged bundle: %w", err)
	}
	committed = true
	_ = os.Remove(temporary)
	return stagedResult(verified, destination, false), nil
}

func stagedResult(verified productrelease.VerifiedArtifact, destination string, cacheHit bool) StagedBundle {
	return StagedBundle{Product: productrelease.Product, Version: verified.Version, Target: verified.Target, Path: destination, CacheHit: cacheHit}
}

func validateVerifiedArtifact(verified productrelease.VerifiedArtifact) error {
	if verified.Product != productrelease.Product || productrelease.ValidateVersion(verified.Version) != nil || productrelease.ValidateTarget(verified.Target) != nil {
		return errors.New("verified artifact identity is invalid")
	}
	if !digestPattern.MatchString(verified.SHA256) || verified.Size < 1 || verified.CachePath == "" || !verified.ProvenanceVerified || !verified.SBOMVerified {
		return errors.New("artifact must have verified digest, size, provenance, SBOM, and cache path before staging")
	}
	file, err := os.Open(verified.CachePath)
	if err != nil {
		return fmt.Errorf("open verified artifact: %w", err)
	}
	defer file.Close()
	details, err := file.Stat()
	if err != nil || details.Size() != verified.Size {
		return errors.New("verified artifact size changed before staging")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return fmt.Errorf("hash verified artifact: %w", err)
	}
	if hex.EncodeToString(hash.Sum(nil)) != verified.SHA256 {
		return errors.New("verified artifact digest changed before staging")
	}
	return nil
}

func targetFamily(target string) string {
	if target == "local-compose" || target == "remote-linux-compose" {
		return "compose"
	}
	return target
}

func extractArchive(archivePath, temporary, family string) ([]stagedFile, error) {
	file, err := os.Open(archivePath)
	if err != nil {
		return nil, fmt.Errorf("open release bundle: %w", err)
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return nil, fmt.Errorf("open release bundle gzip stream: %w", err)
	}
	defer gzipReader.Close()
	tarReader := tar.NewReader(gzipReader)
	var extractionRoot *os.Root
	if temporary != "" {
		extractionRoot, err = os.OpenRoot(temporary)
		if err != nil {
			return nil, fmt.Errorf("open root-scoped extraction directory: %w", err)
		}
		defer extractionRoot.Close()
	}
	seen := make(map[string]bool)
	var files []stagedFile
	var expanded int64
	for count := 0; ; count++ {
		if count >= maximumFiles {
			return nil, fmt.Errorf("release bundle exceeds the %d-entry safety limit", maximumFiles)
		}
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read release bundle: %w", err)
		}
		relative, err := safeArchivePath(header.Name, family)
		if err != nil {
			return nil, err
		}
		key := strings.ToLower(relative)
		if seen[key] {
			return nil, fmt.Errorf("release bundle contains duplicate path %q", relative)
		}
		seen[key] = true
		rootedPath := filepath.FromSlash(relative)
		switch header.Typeflag {
		case tar.TypeDir:
			if temporary != "" {
				if err := extractionRoot.MkdirAll(rootedPath, 0o700); err != nil {
					return nil, err
				}
			}
		case tar.TypeReg, tar.TypeRegA:
			if header.Size < 0 || header.Size > maximumFileSize || expanded+header.Size > maximumExpandedSize {
				return nil, fmt.Errorf("release bundle file %q exceeds expansion safety limits", relative)
			}
			expanded += header.Size
			hash := sha256.New()
			if temporary == "" {
				written, copyErr := io.CopyN(hash, tarReader, header.Size)
				if copyErr != nil || written != header.Size {
					return nil, fmt.Errorf("inspect release bundle file %q", relative)
				}
				files = append(files, stagedFile{Path: relative, Size: written, SHA256: hex.EncodeToString(hash.Sum(nil))})
				continue
			}
			if err := extractionRoot.MkdirAll(filepath.Dir(rootedPath), 0o700); err != nil {
				return nil, err
			}
			output, err := extractionRoot.OpenFile(rootedPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if err != nil {
				return nil, err
			}
			written, copyErr := io.CopyN(io.MultiWriter(output, hash), tarReader, header.Size)
			closeErr := output.Close()
			if copyErr != nil || written != header.Size || closeErr != nil {
				return nil, fmt.Errorf("extract release bundle file %q", relative)
			}
			files = append(files, stagedFile{Path: relative, Size: written, SHA256: hex.EncodeToString(hash.Sum(nil))})
		default:
			return nil, fmt.Errorf("release bundle contains unsupported link or special entry %q", relative)
		}
	}
	if len(files) == 0 {
		return nil, errors.New("release bundle contains no files")
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

func safeArchivePath(name, family string) (string, error) {
	if name == "" || strings.Contains(name, "\\") || strings.ContainsAny(name, "\x00\r\n") || strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("release bundle contains unsafe path %q", name)
	}
	cleaned := path.Clean(strings.TrimPrefix(name, "./"))
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || (cleaned != family && !strings.HasPrefix(cleaned, family+"/")) {
		return "", fmt.Errorf("release bundle path %q is outside expected root %q", name, family)
	}
	if path.Base(cleaned) == stageManifestName {
		return "", fmt.Errorf("release bundle uses reserved staging path %q", name)
	}
	for _, component := range strings.Split(cleaned, "/") {
		if component == "" || component == "." || component == ".." || strings.Contains(component, ":") {
			return "", fmt.Errorf("release bundle contains unsafe path %q", name)
		}
	}
	return cleaned, nil
}

func validateBundleIdentity(root string, verified productrelease.VerifiedArtifact, family string) error {
	identity, err := readIdentity(root)
	if err != nil {
		return err
	}
	return validateIdentity(identity, root, verified.Version, family)
}

func ReadIdentity(staged StagedBundle) (Identity, error) {
	if staged.Product != productrelease.Product || productrelease.ValidateVersion(staged.Version) != nil || productrelease.ValidateTarget(staged.Target) != nil || strings.TrimSpace(staged.Path) == "" {
		return Identity{}, errors.New("staged bundle identity is invalid")
	}
	identity, err := readIdentity(staged.Path)
	if err != nil {
		return Identity{}, err
	}
	if err := validateIdentity(identity, staged.Path, staged.Version, targetFamily(staged.Target)); err != nil {
		return Identity{}, err
	}
	return identity, nil
}

func readIdentity(root string) (Identity, error) {
	contents, err := os.ReadFile(filepath.Join(root, "bundle.json"))
	if err != nil {
		return Identity{}, fmt.Errorf("read bundle identity: %w", err)
	}
	var identity Identity
	decoder := json.NewDecoder(strings.NewReader(string(contents)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&identity); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return Identity{}, errors.New("bundle identity is invalid")
	}
	return identity, nil
}

func validateIdentity(identity Identity, root, version, family string) error {
	if identity.SchemaVersion != "1.0" || identity.Product != productrelease.Product || identity.Version != version || identity.TargetFamily != family {
		return errors.New("bundle identity does not match the verified release")
	}
	switch family {
	case "compose":
		if !pinnedImage(identity.Image) || !pinnedImage(identity.PostgresImage) || identity.PublicHTTPS != "operator-provided" || identity.DefaultBindAddress != "127.0.0.1" {
			return errors.New("Compose bundle security contract is invalid")
		}
		if !regularFile(filepath.Join(root, "compose.yaml")) || !regularFile(filepath.Join(root, ".env.example")) {
			return errors.New("Compose bundle is missing its deployment files")
		}
	case "azure-container-apps":
		if !pinnedImage(identity.Image) || identity.PublicHTTPS != "azure-managed-ingress" || identity.RegistryAuthentication != "none-for-public-ghcr" {
			return errors.New("Azure Container Apps bundle security contract is invalid")
		}
		if !regularFile(filepath.Join(root, "main.json")) {
			return errors.New("Azure Container Apps bundle is missing main.json")
		}
	case "cloudflare":
		if identity.PublicHTTPS != "cloudflare-managed" || identity.Worker != "worker.js" || identity.AssetsDirectory != "assets" || identity.MigrationsDirectory != "migrations" {
			return errors.New("Cloudflare bundle security contract is invalid")
		}
		if !regularFile(filepath.Join(root, identity.Worker)) || !directory(filepath.Join(root, identity.AssetsDirectory)) || !directory(filepath.Join(root, identity.MigrationsDirectory)) {
			return errors.New("Cloudflare bundle is missing Worker, assets, or migrations")
		}
	default:
		return errors.New("unsupported bundle target family")
	}
	return nil
}

func regularFile(filename string) bool {
	details, err := os.Stat(filename)
	return err == nil && details.Mode().IsRegular()
}

func directory(filename string) bool {
	details, err := os.Stat(filename)
	return err == nil && details.IsDir()
}

func pinnedImage(value string) bool {
	parts := strings.Split(value, "@sha256:")
	return len(parts) == 2 && parts[0] != "" && digestPattern.MatchString(parts[1])
}

func verifyStaged(destination string, verified productrelease.VerifiedArtifact, archiveFiles []stagedFile) error {
	contents, err := os.ReadFile(filepath.Join(destination, stageManifestName))
	if err != nil {
		return err
	}
	var manifest stageManifest
	decoder := json.NewDecoder(strings.NewReader(string(contents)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("invalid stage manifest")
	}
	if manifest.SchemaVersion != "1.0" || manifest.Product != productrelease.Product || manifest.Version != verified.Version || manifest.Target != verified.Target || manifest.ArtifactSHA256 != verified.SHA256 || len(archiveFiles) == 0 {
		return errors.New("staged bundle identity mismatch")
	}
	expected := make(map[string]stagedFile, len(archiveFiles))
	for _, item := range archiveFiles {
		if _, err := safeArchivePath(item.Path, targetFamily(verified.Target)); err != nil || !digestPattern.MatchString(item.SHA256) || item.Size < 0 {
			return errors.New("invalid staged file record")
		}
		expected[strings.ToLower(filepath.Clean(filepath.FromSlash(item.Path)))] = item
	}
	seen := 0
	err = filepath.WalkDir(destination, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("staged bundle contains a symbolic link")
		}
		relative, err := filepath.Rel(filepath.Dir(destination), current)
		if err != nil {
			return err
		}
		if filepath.Base(current) == stageManifestName {
			return nil
		}
		item, ok := expected[strings.ToLower(filepath.Clean(relative))]
		if !ok {
			return fmt.Errorf("unexpected staged file %q", relative)
		}
		matches, err := fileMatches(current, item)
		if err != nil || !matches {
			return fmt.Errorf("staged file %q failed verification", relative)
		}
		seen++
		return nil
	})
	if err != nil {
		return err
	}
	if seen != len(expected) {
		return errors.New("staged bundle is missing files")
	}
	return validateBundleIdentity(destination, verified, targetFamily(verified.Target))
}

func fileMatches(filename string, expected stagedFile) (bool, error) {
	file, err := os.Open(filename)
	if err != nil {
		return false, err
	}
	defer file.Close()
	details, err := file.Stat()
	if err != nil || details.Size() != expected.Size || !details.Mode().IsRegular() {
		return false, err
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return false, err
	}
	return hex.EncodeToString(hash.Sum(nil)) == expected.SHA256, nil
}

func ensureWithin(root, candidate string) error {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) || filepath.IsAbs(relative) {
		return errors.New("staging path escapes the dedicated staging root")
	}
	return nil
}

func rejectSymlinkComponents(root, candidate string) error {
	relative, err := filepath.Rel(root, candidate)
	if err != nil {
		return err
	}
	current := root
	for _, component := range strings.Split(relative, string(os.PathSeparator)) {
		current = filepath.Join(current, component)
		details, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if details.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("staging path contains symbolic link %q", current)
		}
	}
	return nil
}
