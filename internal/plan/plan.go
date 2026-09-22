package plan

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path"
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
	versionPattern            = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(?:[-+][A-Za-z0-9.-]+)?$`)
	digestPattern             = regexp.MustCompile(`^[a-f0-9]{64}$`)
	namePattern               = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._-]{0,119}$`)
	keyPattern                = regexp.MustCompile(`^[a-z][A-Za-z0-9]{0,63}$`)
	dockerProjectPattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
	azureSubscriptionPattern  = regexp.MustCompile(`^[a-fA-F0-9]{8}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{12}$`)
	azureResourceGroupPattern = regexp.MustCompile(`^[A-Za-z0-9._()-]{1,90}$`)
	azureLocationPattern      = regexp.MustCompile(`^[a-z0-9]{2,32}$`)
	containerAppNamePattern   = regexp.MustCompile(`^[a-z][a-z0-9-]{1,30}[a-z0-9]$`)
	cloudflareAccountPattern  = regexp.MustCompile(`^[a-fA-F0-9]{32}$`)
	cloudflareWorkerPattern   = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
	cloudflareDatabasePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
	remoteHostPattern         = regexp.MustCompile(`(?i)^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)*$`)
	remoteUserPattern         = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
	remotePathPattern         = regexp.MustCompile(`^/(?:[A-Za-z0-9._-]+/)+[A-Za-z0-9._-]+$`)
	sshFingerprintPattern     = regexp.MustCompile(`^SHA256:[A-Za-z0-9+/]{43}$`)
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
	if p.Target.Kind == "azure-container-apps" {
		if !azureSubscriptionPattern.MatchString(p.Configuration["subscriptionId"]) {
			return errors.New("azure-container-apps subscriptionId must be an Azure subscription UUID")
		}
		if !azureResourceGroupPattern.MatchString(p.Configuration["resourceGroup"]) || strings.HasSuffix(p.Configuration["resourceGroup"], ".") {
			return errors.New("azure-container-apps resourceGroup is invalid")
		}
		if !azureLocationPattern.MatchString(p.Configuration["location"]) {
			return errors.New("azure-container-apps location must be a canonical Azure region name")
		}
		if !containerAppNamePattern.MatchString(p.Configuration["environmentName"]) || !containerAppNamePattern.MatchString(p.Configuration["appName"]) {
			return errors.New("Azure Container Apps names must use 3-32 lowercase letters, numbers, or hyphens")
		}
	}
	if p.Target.Kind == "cloudflare" {
		if !cloudflareAccountPattern.MatchString(p.Configuration["accountId"]) {
			return errors.New("cloudflare accountId must contain 32 hexadecimal characters")
		}
		if !cloudflareWorkerPattern.MatchString(p.Configuration["workerName"]) {
			return errors.New("cloudflare workerName must be a DNS-safe lowercase Worker name")
		}
		if !cloudflareDatabasePattern.MatchString(p.Configuration["databaseName"]) {
			return errors.New("cloudflare databaseName must use lowercase letters, numbers, underscore, or hyphen")
		}
	}
	if p.Target.Kind == "remote-linux-compose" {
		host := p.Configuration["host"]
		if net.ParseIP(host) == nil && !remoteHostPattern.MatchString(host) {
			return errors.New("remote-linux-compose host must be an IP address or DNS hostname")
		}
		port, err := strconv.Atoi(p.Configuration["port"])
		if err != nil || port < 1 || port > 65535 {
			return errors.New("remote-linux-compose port must be from 1 through 65535")
		}
		if !remoteUserPattern.MatchString(p.Configuration["user"]) {
			return errors.New("remote-linux-compose user must be a safe Linux account name")
		}
		directory := p.Configuration["installDirectory"]
		if len(directory) > 200 || path.Clean(directory) != directory || !remotePathPattern.MatchString(directory) {
			return errors.New("remote-linux-compose installDirectory must be a normalized absolute path with at least two safe segments")
		}
		for _, component := range strings.Split(strings.TrimPrefix(directory, "/"), "/") {
			if component == "." || component == ".." {
				return errors.New("remote-linux-compose installDirectory cannot contain dot segments")
			}
		}
		if !dockerProjectPattern.MatchString(p.Configuration["projectName"]) {
			return errors.New("remote-linux-compose projectName must use lowercase letters, numbers, underscore, or hyphen")
		}
		if !sshFingerprintPattern.MatchString(p.Configuration["hostKeySha256"]) {
			return errors.New("remote-linux-compose hostKeySha256 must be an SHA-256 SSH fingerprint")
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
