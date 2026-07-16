package plan

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
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
	Version     string `json:"version"`
	ManifestURL string `json:"manifestUrl"`
	ManifestSHA string `json:"manifestSha256,omitempty"`
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
	if strings.TrimSpace(p.Name) == "" {
		return errors.New("name is required")
	}
	if strings.TrimSpace(p.Release.Version) == "" || strings.TrimSpace(p.Release.ManifestURL) == "" {
		return errors.New("release.version and release.manifestUrl are required")
	}
	if !strings.HasPrefix(p.Release.Version, "v") {
		return errors.New("release.version must be an explicit immutable version beginning with v")
	}
	if !strings.HasPrefix(strings.ToLower(p.Release.ManifestURL), "https://") {
		return errors.New("release.manifestUrl must use HTTPS")
	}
	if !supportedTargets[p.Target.Kind] {
		return fmt.Errorf("unsupported target kind %q", p.Target.Kind)
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
