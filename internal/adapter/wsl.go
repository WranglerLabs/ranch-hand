package adapter

import (
	"context"
	"errors"
	"fmt"
	"net"
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
	if _, err := host.Run(ctx, `command -v docker`, nil); err != nil {
		report.State = "prerequisites-installable"
		appendCheck(&report, "wsl-docker-command", false, "Docker Engine is not installed inside the selected WSL distribution. Ranch Hand can install Docker Engine and Compose on supported Ubuntu/Debian distributions.")
		return report
	}
	appendCheck(&report, "wsl-docker-command", true, "The Docker command is installed inside WSL.")
	dockerVersion, err := host.Run(ctx, `docker version --format '{{.Server.Version}}/{{.Server.Os}}/{{.Server.Arch}}'`, nil)
	if err != nil || !strings.Contains(dockerVersion, "/linux/") {
		report.State = "prerequisites-installable"
		appendCheck(&report, "wsl-docker-engine", false, "Docker is installed, but its Linux Engine is not running or the WSL user lacks access. Ranch Hand can repair the supported Ubuntu/Debian prerequisite setup.")
		return report
	}
	appendCheck(&report, "wsl-docker-engine", true, "The WSL user can reach Linux Docker Engine "+dockerVersion+".")
	composeVersion, err := host.Run(ctx, `docker compose version --short`, nil)
	if err != nil || composeVersion == "" {
		report.State = "prerequisites-installable"
		appendCheck(&report, "wsl-docker-compose", false, "Docker Compose v2 is not available. Ranch Hand can install it on supported Ubuntu/Debian distributions.")
		return report
	}
	appendCheck(&report, "wsl-docker-compose", true, "Docker Compose v2 "+composeVersion+" is available inside WSL.")
	normalized, err := normalizeWSLPlan(ctx, candidate, host)
	if err != nil {
		appendCheck(&report, "wsl-home", false, err.Error())
		return report
	}
	if err := remoteBoundaryAvailable(ctx, host, normalized); err != nil {
		name := "wsl-compose-boundary"
		if errors.Is(err, errComposeInstallDirectoryExists) {
			name = "wsl-directory-boundary"
		}
		appendCheck(&report, name, false, wslBoundaryMessage(candidate.Configuration["projectName"], err))
		return report
	}
	appendCheck(&report, "wsl-compose-boundary", true, "The Compose project and Ranch Hand installation directory are unused.")
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:8080")
	if err != nil {
		appendCheck(&report, "wsl-loopback-port", false, "Windows loopback port 8080 is already in use. Stop the existing service before installing this WSL evaluation target.")
		return report
	}
	_ = listener.Close()
	appendCheck(&report, "wsl-loopback-port", true, "Windows loopback port 8080 is available for RepoWrangler.")
	appendCheck(&report, "wsl-loopback", true, "RepoWrangler will use Docker-managed storage and Windows loopback http://127.0.0.1:8080; no WSL path or IP address is required.")
	report.Ready = true
	return report
}

func (a *WSLCompose) InstallPrerequisites(ctx context.Context, candidate plan.DeploymentPlan, _ Credentials) error {
	if err := candidate.Validate(); err != nil {
		return err
	}
	host, err := connectWSL(ctx, candidate, Credentials{})
	if err != nil {
		return err
	}
	defer host.Close()
	user, err := host.Run(ctx, "id -un", nil)
	if err != nil || !remoteUserPatternForPrerequisites(user) {
		return errors.New("Ranch Hand could not determine a safe WSL user for Docker group access")
	}
	return installWSLDockerPrerequisites(ctx, candidate.Configuration["distribution"], user)
}

func wslBoundaryMessage(project string, err error) string {
	switch {
	case errors.Is(err, errComposeInstallDirectoryExists):
		return "The local WSL installation directory already exists. Ranch Hand will not replace it. Choose a different Compose project name or manage the existing deployment separately."
	case errors.Is(err, errComposeContainersExist):
		return "The local WSL Compose project \"" + project + "\" already has containers. Ranch Hand will not replace them. Choose a different Compose project name or manage the existing deployment separately."
	case errors.Is(err, errComposeVolumesExist):
		return "The local WSL Compose project \"" + project + "\" already has volumes. Ranch Hand will not replace them. Choose a different Compose project name or manage the existing deployment separately."
	default:
		return err.Error()
	}
}

func normalizeWSLPlan(ctx context.Context, candidate plan.DeploymentPlan, host remoteHost) (plan.DeploymentPlan, error) {
	home, err := host.Run(ctx, `printenv HOME`, nil)
	if err != nil || !strings.HasPrefix(home, "/") || strings.ContainsAny(home, "\r\n\x00") {
		return plan.DeploymentPlan{}, errors.New("Ranch Hand could not determine the WSL user's home directory")
	}
	project := candidate.Configuration["projectName"]
	candidate.Target.Kind = "remote-linux-compose"
	normalizedConfiguration := map[string]string{
		"host": candidate.Configuration["distribution"], "port": "22", "user": "wsl",
		"installDirectory": home + "/." + project + "-ranch-hand", "projectName": project,
		"hostKeySha256": "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
	}
	if demoMode := candidate.Configuration["demoMode"]; demoMode != "" {
		normalizedConfiguration["demoMode"] = demoMode
	}
	candidate.Configuration = normalizedConfiguration
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
	if err := candidate.Validate(); err != nil {
		return err
	}
	identity, err := bundle.ReadIdentity(staged)
	if err != nil {
		return err
	}
	runtimeImage, err := prepareWSLCompanion(ctx, candidate.Configuration["distribution"], identity.Image)
	if err != nil {
		return fmt.Errorf("prepare verified public WSL image: %w", err)
	}
	normalized, err := a.normalized(ctx, candidate)
	if err != nil {
		return err
	}
	staged.Target = "remote-linux-compose"
	return a.delegate.apply(ctx, kind, normalized, staged, backups, credentials, runtimeImage)
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

func (a *WSLCompose) CleanupRemnant(ctx context.Context, candidate plan.DeploymentPlan, credentials Credentials) error {
	normalized, err := a.normalized(ctx, candidate)
	if err != nil {
		return err
	}
	return a.delegate.CleanupRemnant(ctx, normalized, credentials)
}
