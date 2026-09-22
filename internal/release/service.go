package release

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	maxManifestSize   = 1 << 20
	maxArtifactSize   = int64(4 << 30)
	maxProvenanceSize = 8 << 20
	maxSBOMSize       = 64 << 20
)

var defaultTrustedHosts = []string{
	"api.github.com",
	"github.com",
	"objects.githubusercontent.com",
	"release-assets.githubusercontent.com",
}

var safeArtifactFilename = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,199}$`)

const officialReleaseBaseURL = "https://github.com/WranglerLabs/repo-wrangler/releases/download"

type Request struct {
	ManifestURL    string `json:"manifestUrl"`
	ManifestSHA256 string `json:"manifestSha256,omitempty"`
	Version        string `json:"version"`
	Target         string `json:"target"`
}

type VerifiedArtifact struct {
	Product            string `json:"product"`
	Version            string `json:"version"`
	Target             string `json:"target"`
	URL                string `json:"url"`
	SHA256             string `json:"sha256"`
	Size               int64  `json:"size"`
	MediaType          string `json:"mediaType,omitempty"`
	AttestationURL     string `json:"attestationUrl,omitempty"`
	SBOMURL            string `json:"sbomUrl,omitempty"`
	CachePath          string `json:"cachePath"`
	CacheHit           bool   `json:"cacheHit"`
	ProvenancePath     string `json:"provenancePath"`
	SBOMPath           string `json:"sbomPath"`
	ProvenanceVerified bool   `json:"provenanceVerified"`
	SBOMVerified       bool   `json:"sbomVerified"`
	ManifestURL        string `json:"manifestUrl"`
	ManifestSHA256     string `json:"manifestSha256"`
}

type Service struct {
	client       *http.Client
	cacheRoot    string
	trustedHosts map[string]bool
	releaseBase  *url.URL
	provenance   ProvenanceVerifier
}

func NewService(cacheRoot string) (*Service, error) {
	return NewServiceWithClient(cacheRoot, &http.Client{Timeout: 10 * time.Minute}, defaultTrustedHosts, officialReleaseBaseURL)
}

func NewServiceWithClient(cacheRoot string, client *http.Client, trustedHosts []string, releaseBaseURL string) (*Service, error) {
	if client == nil {
		return nil, errors.New("HTTP client is required")
	}
	if cacheRoot == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			return nil, fmt.Errorf("locate user cache: %w", err)
		}
		cacheRoot = filepath.Join(base, "WranglerLabs", "Ranch Hand", "artifacts")
	}
	hosts := make(map[string]bool, len(trustedHosts))
	for _, host := range trustedHosts {
		hosts[strings.ToLower(host)] = true
	}
	if len(hosts) == 0 {
		return nil, errors.New("at least one trusted release host is required")
	}
	releaseBase, err := url.Parse(releaseBaseURL)
	if err != nil || releaseBase.Scheme != "https" || releaseBase.Host == "" || !hosts[strings.ToLower(releaseBase.Hostname())] {
		return nil, errors.New("release base must be an absolute HTTPS URL on a trusted host")
	}
	releaseBase.RawQuery = ""
	releaseBase.Fragment = ""
	provenance, err := NewSigstoreProvenanceVerifier(cacheRoot)
	if err != nil {
		return nil, err
	}

	copyClient := *client
	previousRedirect := client.CheckRedirect
	copyClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many release download redirects")
		}
		if err := validateHTTPSURL(req.URL.String(), hosts); err != nil {
			return fmt.Errorf("untrusted release redirect: %w", err)
		}
		if previousRedirect != nil {
			return previousRedirect(req, via)
		}
		return nil
	}
	return &Service{client: &copyClient, cacheRoot: cacheRoot, trustedHosts: hosts, releaseBase: releaseBase, provenance: provenance}, nil
}

func (s *Service) VerifyAndCache(ctx context.Context, request Request) (VerifiedArtifact, error) {
	if err := ValidateVersion(request.Version); err != nil {
		return VerifiedArtifact{}, err
	}
	if err := ValidateTarget(request.Target); err != nil {
		return VerifiedArtifact{}, err
	}
	manifestURL, err := s.validateManifestURL(request.ManifestURL, request.Version)
	if err != nil {
		return VerifiedArtifact{}, fmt.Errorf("manifest URL: %w", err)
	}
	if request.ManifestSHA256 != "" && !digestPattern.MatchString(request.ManifestSHA256) {
		return VerifiedArtifact{}, errors.New("manifestSha256 must contain 64 hexadecimal characters")
	}

	manifestBytes, err := s.downloadBytes(ctx, manifestURL, maxManifestSize)
	if err != nil {
		return VerifiedArtifact{}, fmt.Errorf("download release manifest: %w", err)
	}
	manifestDigest := sha256.Sum256(manifestBytes)
	if request.ManifestSHA256 != "" && !strings.EqualFold(hex.EncodeToString(manifestDigest[:]), request.ManifestSHA256) {
		return VerifiedArtifact{}, errors.New("release manifest SHA-256 does not match the deployment plan")
	}

	var manifest Manifest
	decoder := json.NewDecoder(bytes.NewReader(manifestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return VerifiedArtifact{}, fmt.Errorf("decode release manifest: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return VerifiedArtifact{}, errors.New("release manifest contains trailing data")
	}
	if err := manifest.Validate(request.Version, s.validateURL); err != nil {
		return VerifiedArtifact{}, fmt.Errorf("validate release manifest: %w", err)
	}
	artifact, err := manifest.Artifact(request.Target)
	if err != nil {
		return VerifiedArtifact{}, err
	}
	if artifact.Size > maxArtifactSize {
		return VerifiedArtifact{}, fmt.Errorf("artifact exceeds the %d-byte safety limit", maxArtifactSize)
	}
	artifactURL, err := s.validateReleaseAssetURL(artifact.URL, request.Version)
	if err != nil {
		return VerifiedArtifact{}, fmt.Errorf("artifact URL for %s: %w", artifact.Target, err)
	}

	cachePath, cacheHit, err := s.cacheArtifact(ctx, request.Version, artifact, artifactURL)
	if err != nil {
		return VerifiedArtifact{}, err
	}
	provenancePath, sbomPath, err := s.verifySupplyChain(ctx, request.Version, artifact, cachePath)
	if err != nil {
		return VerifiedArtifact{}, err
	}
	return VerifiedArtifact{
		Product: Product, Version: request.Version, Target: artifact.Target, URL: artifact.URL,
		SHA256: strings.ToLower(artifact.SHA256), Size: artifact.Size, MediaType: artifact.MediaType,
		AttestationURL: artifact.AttestationURL, SBOMURL: artifact.SBOMURL,
		CachePath: cachePath, CacheHit: cacheHit, ProvenancePath: provenancePath, SBOMPath: sbomPath,
		ProvenanceVerified: true, SBOMVerified: true,
		ManifestURL: manifestURL.String(), ManifestSHA256: hex.EncodeToString(manifestDigest[:]),
	}, nil
}

func (s *Service) verifySupplyChain(ctx context.Context, version string, artifact Artifact, cachePath string) (string, string, error) {
	if artifact.AttestationURL == "" || artifact.SBOMURL == "" {
		return "", "", errors.New("release artifact must include provenance and SBOM URLs")
	}
	provenanceURL, err := s.validateReleaseAssetURL(artifact.AttestationURL, version)
	if err != nil {
		return "", "", fmt.Errorf("provenance URL: %w", err)
	}
	sbomURL, err := s.validateReleaseAssetURL(artifact.SBOMURL, version)
	if err != nil {
		return "", "", fmt.Errorf("SBOM URL: %w", err)
	}
	provenanceJSON, err := s.downloadBytes(ctx, provenanceURL, maxProvenanceSize)
	if err != nil {
		return "", "", fmt.Errorf("download provenance bundle: %w", err)
	}
	sbomJSON, err := s.downloadBytes(ctx, sbomURL, maxSBOMSize)
	if err != nil {
		return "", "", fmt.Errorf("download SBOM: %w", err)
	}
	if err := validateSPDX(sbomJSON); err != nil {
		return "", "", err
	}
	sbomDigest := sha256.Sum256(sbomJSON)
	if s.provenance == nil {
		return "", "", errors.New("provenance verification is unavailable")
	}
	if err := s.provenance.Verify(provenanceJSON, artifact.SHA256); err != nil {
		return "", "", fmt.Errorf("verify artifact provenance: %w", err)
	}
	if err := s.provenance.Verify(provenanceJSON, hex.EncodeToString(sbomDigest[:])); err != nil {
		return "", "", fmt.Errorf("verify SBOM provenance: %w", err)
	}

	directory := filepath.Dir(cachePath)
	provenancePath := filepath.Join(directory, artifactFilename(provenanceURL.String(), "provenance"))
	sbomPath := filepath.Join(directory, artifactFilename(sbomURL.String(), "sbom"))
	if err := atomicWrite(provenancePath, provenanceJSON); err != nil {
		return "", "", fmt.Errorf("cache verified provenance: %w", err)
	}
	if err := atomicWrite(sbomPath, sbomJSON); err != nil {
		return "", "", fmt.Errorf("cache verified SBOM: %w", err)
	}
	return provenancePath, sbomPath, nil
}

func validateSPDX(contents []byte) error {
	var document struct {
		SPDXVersion string            `json:"spdxVersion"`
		DataLicense string            `json:"dataLicense"`
		SPDXID      string            `json:"SPDXID"`
		Name        string            `json:"name"`
		Packages    []json.RawMessage `json:"packages"`
	}
	if err := json.Unmarshal(contents, &document); err != nil {
		return fmt.Errorf("decode SPDX SBOM: %w", err)
	}
	if !strings.HasPrefix(document.SPDXVersion, "SPDX-2.") || document.DataLicense != "CC0-1.0" || document.SPDXID != "SPDXRef-DOCUMENT" || strings.TrimSpace(document.Name) == "" || len(document.Packages) == 0 {
		return errors.New("SBOM is not a complete SPDX 2.x document")
	}
	return nil
}

func (s *Service) validateURL(raw string) error {
	return validateHTTPSURL(raw, s.trustedHosts)
}

func (s *Service) validateManifestURL(raw, version string) (*url.URL, error) {
	expected := s.releaseURL(version, "release-manifest.json")
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || !strings.EqualFold(parsed.Scheme, expected.Scheme) || !strings.EqualFold(parsed.Host, expected.Host) || !strings.EqualFold(parsed.Path, expected.Path) {
		return nil, errors.New("must identify the official versioned RepoWrangler release manifest")
	}
	return expected, nil
}

func (s *Service) validateReleaseAssetURL(raw, version string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || !strings.EqualFold(parsed.Scheme, s.releaseBase.Scheme) || !strings.EqualFold(parsed.Host, s.releaseBase.Host) {
		return nil, errors.New("must identify an official versioned RepoWrangler release asset")
	}
	filename := path.Base(parsed.Path)
	if !safeArtifactFilename.MatchString(filename) || filename == "." || filename == ".." {
		return nil, errors.New("contains an unsafe filename")
	}
	expected := s.releaseURL(version, filename)
	if !strings.EqualFold(parsed.Path, expected.Path) {
		return nil, errors.New("must identify an official versioned RepoWrangler release asset")
	}
	return expected, nil
}

func (s *Service) releaseURL(version, filename string) *url.URL {
	result := *s.releaseBase
	result.Path = "/" + strings.TrimPrefix(path.Join(s.releaseBase.Path, version, filename), "/")
	return &result
}

func (s *Service) downloadBytes(ctx context.Context, destination *url.URL, maximum int64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, destination.String(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	response, err := s.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("release host returned HTTP %d", response.StatusCode)
	}
	contents, err := io.ReadAll(io.LimitReader(response.Body, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(contents)) > maximum {
		return nil, fmt.Errorf("response exceeds the %d-byte safety limit", maximum)
	}
	return contents, nil
}

func (s *Service) cacheArtifact(ctx context.Context, version string, artifact Artifact, downloadURL *url.URL) (string, bool, error) {
	filename := artifactFilename(downloadURL.String(), artifact.Target)
	directory := filepath.Join(s.cacheRoot, version, strings.ToLower(artifact.SHA256))
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", false, fmt.Errorf("create artifact cache: %w", err)
	}
	destination := filepath.Join(directory, filename)
	if matches, err := verifyFile(destination, artifact); err == nil && matches {
		return destination, true, nil
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", false, fmt.Errorf("verify cached artifact: %w", err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL.String(), nil)
	if err != nil {
		return "", false, err
	}
	request.Header.Set("Accept", "application/octet-stream")
	response, err := s.client.Do(request)
	if err != nil {
		return "", false, fmt.Errorf("download release artifact: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", false, fmt.Errorf("release host returned HTTP %d for artifact", response.StatusCode)
	}

	temporary, err := os.CreateTemp(directory, ".download-*")
	if err != nil {
		return "", false, fmt.Errorf("create temporary artifact: %w", err)
	}
	temporaryPath := temporary.Name()
	keep := false
	defer func() {
		temporary.Close()
		if !keep {
			_ = os.Remove(temporaryPath)
		}
	}()

	hash := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(temporary, hash), io.LimitReader(response.Body, artifact.Size+1))
	if copyErr != nil {
		return "", false, fmt.Errorf("write release artifact: %w", copyErr)
	}
	if written != artifact.Size {
		return "", false, fmt.Errorf("artifact size mismatch: expected %d bytes, received %d", artifact.Size, written)
	}
	if !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), artifact.SHA256) {
		return "", false, errors.New("artifact SHA-256 mismatch")
	}
	if err := temporary.Sync(); err != nil {
		return "", false, fmt.Errorf("flush release artifact: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return "", false, fmt.Errorf("close release artifact: %w", err)
	}
	_ = os.Remove(destination)
	if err := os.Rename(temporaryPath, destination); err != nil {
		return "", false, fmt.Errorf("commit release artifact to cache: %w", err)
	}
	keep = true
	return destination, false, nil
}

func verifyFile(path string, artifact Artifact) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer file.Close()
	details, err := file.Stat()
	if err != nil {
		return false, err
	}
	if details.Size() != artifact.Size {
		return false, nil
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return false, err
	}
	return strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), artifact.SHA256), nil
}

func atomicWrite(destination string, contents []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".evidence-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if _, err := temporary.Write(contents); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	_ = os.Remove(destination)
	if err := os.Rename(temporaryPath, destination); err != nil {
		return err
	}
	committed = true
	return nil
}

func pathFromURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return parsed.Path
}

func artifactFilename(raw, target string) string {
	filename := path.Base(strings.TrimSpace(pathFromURL(raw)))
	if !safeArtifactFilename.MatchString(filename) || filename == "." || filename == ".." {
		return target + ".bundle"
	}
	return filename
}
