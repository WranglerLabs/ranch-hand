package release

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const (
	SchemaVersion = "1.0"
	Product       = "RepoWrangler"
)

var (
	versionPattern   = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(?:[-+][A-Za-z0-9.-]+)?$`)
	digestPattern    = regexp.MustCompile(`^[a-fA-F0-9]{64}$`)
	supportedTargets = map[string]bool{
		"azure-container-apps": true,
		"cloudflare":           true,
		"local-compose":        true,
		"local-wsl-compose":    true,
		"remote-linux-compose": true,
	}
)

type Manifest struct {
	SchemaVersion string     `json:"schemaVersion"`
	Product       string     `json:"product"`
	Version       string     `json:"version"`
	ReleasedAt    string     `json:"releasedAt"`
	Artifacts     []Artifact `json:"artifacts"`
}

type Artifact struct {
	Target         string `json:"target"`
	URL            string `json:"url"`
	SHA256         string `json:"sha256"`
	Size           int64  `json:"size"`
	MediaType      string `json:"mediaType,omitempty"`
	AttestationURL string `json:"attestationUrl,omitempty"`
	SBOMURL        string `json:"sbomUrl,omitempty"`
}

func ValidateVersion(value string) error {
	if !versionPattern.MatchString(value) {
		return errors.New("version must be an explicit semantic version beginning with v")
	}
	return nil
}

func ValidateTarget(value string) error {
	if !supportedTargets[value] {
		return fmt.Errorf("unsupported target %q", value)
	}
	return nil
}

// ArtifactTarget maps an operator target to its published artifact family.
func ArtifactTarget(value string) string {
	if value == "local-wsl-compose" {
		return "local-compose"
	}
	return value
}

func (m Manifest) Validate(expectedVersion string, validateURL func(string) error) error {
	if m.SchemaVersion != SchemaVersion {
		return fmt.Errorf("manifest schemaVersion must be %q", SchemaVersion)
	}
	if m.Product != Product {
		return fmt.Errorf("manifest product must be %q", Product)
	}
	if err := ValidateVersion(m.Version); err != nil {
		return fmt.Errorf("manifest %w", err)
	}
	if m.Version != expectedVersion {
		return fmt.Errorf("manifest version %q does not match requested version %q", m.Version, expectedVersion)
	}
	if _, err := time.Parse(time.RFC3339, m.ReleasedAt); err != nil {
		return errors.New("manifest releasedAt must be an RFC3339 date-time")
	}
	if len(m.Artifacts) == 0 {
		return errors.New("manifest must contain at least one artifact")
	}

	seen := make(map[string]bool, len(m.Artifacts))
	for _, artifact := range m.Artifacts {
		if err := ValidateTarget(artifact.Target); err != nil {
			return err
		}
		if seen[artifact.Target] {
			return fmt.Errorf("manifest contains duplicate target %q", artifact.Target)
		}
		seen[artifact.Target] = true
		if err := validateURL(artifact.URL); err != nil {
			return fmt.Errorf("artifact URL for %s: %w", artifact.Target, err)
		}
		if !digestPattern.MatchString(artifact.SHA256) {
			return fmt.Errorf("artifact sha256 for %s must contain 64 hexadecimal characters", artifact.Target)
		}
		if artifact.Size < 1 {
			return fmt.Errorf("artifact size for %s must be positive", artifact.Target)
		}
		for name, value := range map[string]string{"attestation URL": artifact.AttestationURL, "SBOM URL": artifact.SBOMURL} {
			if value != "" {
				if err := validateURL(value); err != nil {
					return fmt.Errorf("%s for %s: %w", name, artifact.Target, err)
				}
			}
		}
	}
	return nil
}

func (m Manifest) Artifact(target string) (Artifact, error) {
	for _, artifact := range m.Artifacts {
		if artifact.Target == target {
			return artifact, nil
		}
	}
	return Artifact{}, fmt.Errorf("release %s has no artifact for target %q", m.Version, target)
}

func validateHTTPSURL(raw string, allowedHosts map[string]bool) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return errors.New("must be an absolute HTTPS URL")
	}
	if parsed.User != nil {
		return errors.New("must not contain user information")
	}
	host := strings.ToLower(parsed.Hostname())
	if !allowedHosts[host] {
		return fmt.Errorf("host %q is not trusted", host)
	}
	return nil
}
