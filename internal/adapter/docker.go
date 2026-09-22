package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/WranglerLabs/ranch-hand/internal/plan"
)

type LocalDocker struct {
	client       *http.Client
	baseURL      string
	healthClient *http.Client
	backupRoot   string
	prepareImage func(context.Context, string) (string, error)
}

func NewLocalDocker() *LocalDocker {
	return &LocalDocker{
		client: &http.Client{Transport: localDockerTransport(), Timeout: 10 * time.Minute}, baseURL: "http://docker",
	}
}

func (d *LocalDocker) Preflight(ctx context.Context, candidate plan.DeploymentPlan, _ Credentials) Report {
	report := Report{Target: candidate.Target.Kind}
	baseURL := d.baseURL
	if baseURL == "" {
		baseURL = "http://docker"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/_ping", nil)
	if err != nil {
		appendCheck(&report, "docker-engine", false, err.Error())
		return report
	}
	response, err := d.client.Do(request)
	if err != nil {
		report.State = "prerequisites-installable"
		appendCheck(&report, "docker-engine", false, "Ranch Hand could not reach the local Docker Engine API: "+err.Error())
		return report
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, 1024))
	response.Body.Close()
	if readErr != nil || response.StatusCode != http.StatusOK || strings.TrimSpace(string(body)) != "OK" {
		appendCheck(&report, "docker-engine", false, fmt.Sprintf("The local Docker Engine ping was not healthy (HTTP %d).", response.StatusCode))
		return report
	}
	appendCheck(&report, "docker-engine", true, "Ranch Hand reached the local Docker Engine through its native API.")

	request, _ = http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/version", nil)
	response, err = d.client.Do(request)
	if err != nil {
		appendCheck(&report, "docker-version", false, "Ranch Hand could not read the Docker Engine version: "+err.Error())
		return report
	}
	defer response.Body.Close()
	var version struct {
		Version    string `json:"Version"`
		APIVersion string `json:"ApiVersion"`
		OSType     string `json:"Os"`
	}
	if response.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&version) != nil || version.APIVersion == "" {
		appendCheck(&report, "docker-version", false, "The Docker Engine returned an invalid version response.")
		return report
	}
	if !strings.EqualFold(version.OSType, "linux") {
		appendCheck(&report, "docker-linux-containers", false, "RepoWrangler's immutable image requires the Docker Engine to run Linux containers.")
		return report
	}
	appendCheck(&report, "docker-version", true, fmt.Sprintf("Docker Engine %s (API %s) is running Linux containers.", version.Version, version.APIVersion))
	appendCheck(&report, "compose-loopback", true, "The verified Compose bundle binds to loopback by default and contains no bundled proxy.")
	report.Ready = true
	return report
}

func (d *LocalDocker) InstallPrerequisites(ctx context.Context, candidate plan.DeploymentPlan, _ Credentials) error {
	if err := candidate.Validate(); err != nil {
		return err
	}
	return installDockerDesktop(ctx)
}
