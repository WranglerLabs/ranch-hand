package plan

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const CurrentSchemaVersion = "1.0"

var secretKey = regexp.MustCompile(`(?i)(password|passphrase|private.?key|client.?secret|access.?token|api.?key|credential)`)

type DeploymentPlan struct {
	SchemaVersion string            `json:"schemaVersion"`
	Name          string            `json:"name"`
	Release       ReleaseSelection  `json:"release"`
	Target        Target            `json:"target"`
	Configuration map[string]string `json:"configuration,omitempty"`
}

type ReleaseSelection struct {
	Version        string `json:"version"`
	ManifestURL    string `json:"manifestUrl"`
	ManifestSHA256 string `json:"manifestSha256"`
	ArtifactSHA256 string `json:"artifactSha256"`
	ArtifactSize   int64  `json:"artifactSize"`
}

type Target struct {
	Kind string `json:"kind"`
}

var supportedTargets = map[string]bool{
	"azure-container-apps": true,
	"cloudflare":           true,
	"local-compose":        true,
	"remote-linux-compose": true,
}

var (
	versionPattern       = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(?:[-+][A-Za-z0-9.-]+)?$`)
	digestPattern        = regexp.MustCompile(`^[a-f0-9]{64}$`)
	namePattern          = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._-]{0,119}$`)
	keyPattern           = regexp.MustCompile(`^[a-z][A-Za-z0-9]{0,63}$`)
	dockerProjectPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
)

var configurationFields = map[string]map[string]bool{
	"azure-container-apps": {
		"subscriptionId": true, "resourceGroup": true, "location": true,
		"environmentName": true, "appName": true, "customDomain": false,
	},
	"cloudflare": {
		"accountId": true, "workerName": true, "databaseName": true,
		"customDomain": false,
	},
	"local-compose": {
		"projectName": true, "dataVolume": true, "listenAddress": true,
	},
	"remote-linux-compose": {
		"host": true, "port": true, "user": true, "installDirectory": true,
		"projectName": true, "hostKeySha256": true,
	},
}

type CreateRequest struct {
	Name          string            `json:"name"`
	Version       string            `json:"version"`
	Target        string            `json:"target"`
	Configuration map[string]string `json:"configuration"`
}

type VerifiedRelease struct {
	ManifestURL    string
	ManifestSHA256 string
	ArtifactSHA256 string
	ArtifactSize   int64
}

func Create(request CreateRequest, verified VerifiedRelease) (DeploymentPlan, error) {
	candidate := DeploymentPlan{
		SchemaVersion: CurrentSchemaVersion,
		Name:          strings.TrimSpace(request.Name),
		Release: ReleaseSelection{
			Version: request.Version, ManifestURL: verified.ManifestURL,
			ManifestSHA256: strings.ToLower(verified.ManifestSHA256),
			ArtifactSHA256: strings.ToLower(verified.ArtifactSHA256), ArtifactSize: verified.ArtifactSize,
		},
		Target:        Target{Kind: request.Target},
		Configuration: request.Configuration,
	}
	if err := candidate.Validate(); err != nil {
		return DeploymentPlan{}, err
	}
	return candidate, nil
}

func CanonicalJSON(candidate DeploymentPlan) ([]byte, error) {
	if err := candidate.Validate(); err != nil {
		return nil, err
	}
	contents, err := json.MarshalIndent(candidate, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode deployment plan: %w", err)
	}
	return append(contents, '\n'), nil
}

func DecodeAndValidate(data []byte) (DeploymentPlan, error) {
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return DeploymentPlan{}, fmt.Errorf("invalid JSON: %w", err)
	}
	if path := findSecretKey(raw, "$"); path != "" {
		return DeploymentPlan{}, fmt.Errorf("deployment plans must be secret-free; forbidden field at %s", path)
	}

	var candidate DeploymentPlan
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&candidate); err != nil {
		return DeploymentPlan{}, fmt.Errorf("invalid deployment plan: %w", err)
	}
	if err := candidate.Validate(); err != nil {
		return DeploymentPlan{}, err
	}
	return candidate, nil
}

func (p DeploymentPlan) Validate() error {
	if p.SchemaVersion != CurrentSchemaVersion {
		return fmt.Errorf("schemaVersion must be %q", CurrentSchemaVersion)
	}
	if !namePattern.MatchString(p.Name) {
		return errors.New("name must be 1-120 characters and use letters, numbers, spaces, dot, underscore, or hyphen")
	}
	if strings.TrimSpace(p.Release.Version) == "" || strings.TrimSpace(p.Release.ManifestURL) == "" {
		return errors.New("release.version and release.manifestUrl are required")
	}
	if !versionPattern.MatchString(p.Release.Version) {
		return errors.New("release.version must be an explicit immutable semantic version beginning with v")
	}
	if !strings.HasPrefix(strings.ToLower(p.Release.ManifestURL), "https://") {
		return errors.New("release.manifestUrl must use HTTPS")
	}
	expectedManifestURL := fmt.Sprintf("https://github.com/WranglerLabs/repo-wrangler/releases/download/%s/release-manifest.json", p.Release.Version)
	if p.Release.ManifestURL != expectedManifestURL {
		return errors.New("release.manifestUrl must identify the official versioned RepoWrangler release manifest")
	}
	if !digestPattern.MatchString(p.Release.ManifestSHA256) || !digestPattern.MatchString(p.Release.ArtifactSHA256) {
		return errors.New("release manifest and artifact SHA-256 values must contain 64 lowercase hexadecimal characters")
	}
	if p.Release.ArtifactSize < 1 {
		return errors.New("release.artifactSize must be positive")
	}
	if !supportedTargets[p.Target.Kind] {
		return fmt.Errorf("unsupported target kind %q", p.Target.Kind)
	}
	fields := configurationFields[p.Target.Kind]
	for key, value := range p.Configuration {
		if !keyPattern.MatchString(key) || secretKey.MatchString(key) {
			return fmt.Errorf("configuration field %q is not permitted", key)
		}
		required, known := fields[key]
		if !known {
			return fmt.Errorf("configuration field %q is not supported for target %q", key, p.Target.Kind)
		}
		if required && strings.TrimSpace(value) == "" {
			return fmt.Errorf("configuration field %q is required", key)
		}
		if len(value) > 512 || strings.ContainsAny(value, "\r\n\x00") {
			return fmt.Errorf("configuration field %q contains an invalid value", key)
		}
	}
	var missing []string
	for key, required := range fields {
		if required && strings.TrimSpace(p.Configuration[key]) == "" {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("missing required configuration fields: %s", strings.Join(missing, ", "))
	}
	if p.Target.Kind == "local-compose" {
		if !dockerProjectPattern.MatchString(p.Configuration["projectName"]) {
			return errors.New("local-compose projectName must use lowercase letters, numbers, underscore, or hyphen")
		}
		if !dockerProjectPattern.MatchString(p.Configuration["dataVolume"]) {
			return errors.New("local-compose dataVolume must use lowercase letters, numbers, underscore, or hyphen")
		}
		listen := strings.TrimPrefix(p.Configuration["listenAddress"], "127.0.0.1:")
		port, err := strconv.Atoi(listen)
		if err != nil || listen == p.Configuration["listenAddress"] || port < 1024 || port > 65535 {
			return errors.New("local-compose listenAddress must use 127.0.0.1 and a port from 1024 through 65535")
		}
	}
	return nil
}

func findSecretKey(value any, path string) string {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			childPath := path + "." + key
			if secretKey.MatchString(key) {
				return childPath
			}
			if found := findSecretKey(child, childPath); found != "" {
				return found
			}
		}
	case []any:
		for index, child := range typed {
			if found := findSecretKey(child, fmt.Sprintf("%s[%d]", path, index)); found != "" {
				return found
			}
		}
	}
	return ""
}
