package adapter

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/WranglerLabs/ranch-hand/internal/bundle"
	"github.com/WranglerLabs/ranch-hand/internal/lifecycle"
	"github.com/WranglerLabs/ranch-hand/internal/plan"
)

type WSLCompose struct {
	delegate *RemoteLinuxCompose
}

func NewWSLCompose() *WSLCompose {
	return &WSLCompose{delegate: newRemoteLinuxCompose(connectWSL)}
}

func (a *WSLCompose) Preflight(ctx context.Context, candidate plan.DeploymentPlan, _ Credentials) Report {
	report := Report{Target: candidate.Target.Kind}
	if err := candidate.Validate(); err != nil {
		appendCheck(&report, "wsl-plan", false, err.Error())
		return report
	}
	distributions, err := WSLDistributions(ctx)
	if err != nil {
		appendCheck(&report, "wsl", false, err.Error())
		return report
	}
	distribution := candidate.Configuration["distribution"]
	found := false
	for _, installed := range distributions {
		if installed == distribution {
			found = true
			break
		}
	}
	if !found {
		appendCheck(&report, "wsl-distribution", false, fmt.Sprintf("WSL distribution %q is not installed.", distribution))
		return report
	}
	appendCheck(&report, "wsl-distribution", true, "WSL distribution "+distribution+" is installed and starts without SSH.")
	host, err := connectWSL(ctx, candidate, Credentials{})
	if err != nil {
		appendCheck(&report, "wsl-executor", false, err.Error())
		return report
	}
	defer host.Close()
	dockerVersion, err := host.Run(ctx, `docker version --format '{{.Server.Version}}/{{.Server.Os}}/{{.Server.Arch}}'`, nil)
	if err != nil || !strings.Contains(dockerVersion, "/linux/") {
		appendCheck(&report, "wsl-docker-engine", false, "Docker is installed, but its Linux Docker Engine is not running or is unavailable to the WSL user. Start the Docker service inside this distribution and retry.")
		return report
	}
	appendCheck(&report, "wsl-docker-engine", true, "The WSL user can reach Linux Docker Engine "+dockerVersion+".")
	composeVersion, err := host.Run(ctx, `docker compose version --short`, nil)
	if err != nil || composeVersion == "" {
		appendCheck(&report, "wsl-docker-compose", false, "Docker Compose v2 is not available inside the selected WSL distribution.")
		return report
	}
	appendCheck(&report, "wsl-docker-compose", true, "Docker Compose v2 "+composeVersion+" is available inside WSL.")
	normalized, err := normalizeWSLPlan(ctx, candidate, host)
	if err != nil {
		appendCheck(&report, "wsl-home", false, err.Error())
		return report
	}
	if err := remoteBoundaryAvailable(ctx, host, normalized); err != nil {
		appendCheck(&report, "wsl-compose-boundary", false, err.Error())
		return report
	}
	appendCheck(&report, "wsl-compose-boundary", true, "The Compose project and Ranch Hand installation directory are unused.")
	appendCheck(&report, "wsl-loopback", true, "RepoWrangler will use Docker-managed storage and Windows loopback http://127.0.0.1:8080; no WSL path or IP address is required.")
	report.Ready = true
	return report
}

func normalizeWSLPlan(ctx context.Context, candidate plan.DeploymentPlan, host remoteHost) (plan.DeploymentPlan, error) {
	home, err := host.Run(ctx, `printenv HOME`, nil)
	if err != nil || !strings.HasPrefix(home, "/") || strings.ContainsAny(home, "\r\n\x00") {
		return plan.DeploymentPlan{}, errors.New("Ranch Hand could not determine the WSL user's home directory")
	}
	project := candidate.Configuration["projectName"]
	candidate.Target.Kind = "remote-linux-compose"
	candidate.Configuration = map[string]string{
		"host": candidate.Configuration["distribution"], "port": "22", "user": "wsl",
		"installDirectory": home + "/." + project + "-ranch-hand", "projectName": project,
		"hostKeySha256": "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
	}
	return candidate, candidate.Validate()
}

func (a *WSLCompose) normalized(ctx context.Context, candidate plan.DeploymentPlan) (plan.DeploymentPlan, error) {
	host, err := connectWSL(ctx, candidate, Credentials{})
	if err != nil {
		return plan.DeploymentPlan{}, err
	}
	defer host.Close()
	return normalizeWSLPlan(ctx, candidate, host)
}

func (a *WSLCompose) Backup(context.Context, plan.DeploymentPlan, string, Credentials) (lifecycle.BackupArtifact, error) {
	return lifecycle.BackupArtifact{}, errors.New("local WSL Compose backup is not implemented in this Preview")
}

func (a *WSLCompose) Apply(ctx context.Context, kind lifecycle.OperationKind, candidate plan.DeploymentPlan, fromVersion string, staged bundle.StagedBundle, backups lifecycle.OperationBackups, credentials Credentials) error {
	normalized, err := a.normalized(ctx, candidate)
	if err != nil {
		return err
	}
	staged.Target = "remote-linux-compose"
	return a.delegate.Apply(ctx, kind, normalized, fromVersion, staged, backups, credentials)
}

func (a *WSLCompose) Verify(ctx context.Context, candidate plan.DeploymentPlan, credentials Credentials) error {
	normalized, err := a.normalized(ctx, candidate)
	if err != nil {
		return err
	}
	return a.delegate.Verify(ctx, normalized, credentials)
}

func (a *WSLCompose) Recover(ctx context.Context, kind lifecycle.OperationKind, candidate plan.DeploymentPlan, fromVersion string, backups lifecycle.OperationBackups, credentials Credentials) error {
	normalized, err := a.normalized(ctx, candidate)
	if err != nil {
		return err
	}
	return a.delegate.Recover(ctx, kind, normalized, fromVersion, backups, credentials)
}
