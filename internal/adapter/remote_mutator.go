package adapter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/WranglerLabs/ranch-hand/internal/bundle"
	"github.com/WranglerLabs/ranch-hand/internal/lifecycle"
	"github.com/WranglerLabs/ranch-hand/internal/plan"
)

const remoteMarkerName = ".ranch-hand-installation.json"

var remoteDigestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

var (
	errComposeInstallDirectoryExists = errors.New("refusing to replace a pre-existing Compose installation directory")
	errComposeContainersExist        = errors.New("refusing to replace a Compose project with existing containers")
	errComposeVolumesExist           = errors.New("refusing to replace a Compose project with existing volumes")
	errRemoteMarkerUnavailable       = errors.New("remote Ranch Hand ownership marker is unavailable")
	errRemoteMarkerEmpty             = errors.New("remote Ranch Hand ownership marker is empty")
)

type remoteInstallation struct {
	SchemaVersion     string `json:"schemaVersion"`
	DeploymentID      string `json:"deploymentId"`
	Version           string `json:"version"`
	ArtifactSHA256    string `json:"artifactSha256"`
	ProjectName       string `json:"projectName"`
	ContainerName     string `json:"containerName"`
	VolumeName        string `json:"volumeName"`
	Image             string `json:"image"`
	ComposeSHA256     string `json:"composeSha256"`
	OverrideSHA256    string `json:"overrideSha256"`
	EnvironmentSHA256 string `json:"environmentSha256"`
}

func (a *RemoteLinuxCompose) Backup(context.Context, plan.DeploymentPlan, string, Credentials) (lifecycle.BackupArtifact, error) {
	return lifecycle.BackupArtifact{}, errors.New("remote Linux Compose backup is not implemented")
}

func (a *RemoteLinuxCompose) Apply(ctx context.Context, kind lifecycle.OperationKind, candidate plan.DeploymentPlan, _ string, staged bundle.StagedBundle, backups lifecycle.OperationBackups, credentials Credentials) error {
	if kind != lifecycle.Install || backups.Selected != nil || backups.Safety != nil {
		return errors.New("the remote Linux Compose adapter currently supports only a new evaluation install")
	}
	if err := candidate.Validate(); err != nil {
		return err
	}
	identity, err := bundle.ReadIdentity(staged)
	if err != nil {
		return err
	}
	if staged.Target != "remote-linux-compose" {
		return errors.New("remote Linux adapter requires a remote-linux-compose bundle")
	}
	host, err := a.connect(ctx, candidate, credentials)
	if err != nil {
		return err
	}
	defer host.Close()
	if err := remoteBoundaryAvailable(ctx, host, candidate); err != nil {
		return err
	}
	directory := candidate.Configuration["installDirectory"]
	if _, err := host.Run(ctx, "mkdir --mode=700 -- "+shellQuote(directory), nil); err != nil {
		return fmt.Errorf("create dedicated remote installation directory: %w", err)
	}
	compose, err := readBoundedFile(filepath.Join(staged.Path, "compose.yaml"), 4<<20)
	if err != nil {
		return err
	}
	deploymentID, err := lifecycle.DeploymentID(candidate)
	if err != nil {
		return err
	}
	project := candidate.Configuration["projectName"]
	containerName := project + "-server"
	volumeName := project + "-data"
	override := []byte(remoteOverride(project, containerName, volumeName, deploymentID, candidate.Release.Version))
	environment := []byte(remoteEnvironment(candidate.Release.Version))
	marker := remoteInstallation{
		SchemaVersion: "1.0", DeploymentID: deploymentID, Version: candidate.Release.Version,
		ArtifactSHA256: candidate.Release.ArtifactSHA256, ProjectName: project, ContainerName: containerName,
		VolumeName: volumeName, Image: identity.Image, ComposeSHA256: bytesSHA256(compose),
		OverrideSHA256: bytesSHA256(override), EnvironmentSHA256: bytesSHA256(environment),
	}
	markerJSON, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return err
	}
	markerJSON = append(markerJSON, '\n')
	for _, file := range []struct {
		name string
		data []byte
	}{{"compose.yaml", compose}, {"ranch-hand.override.yaml", override}, {".env", environment}, {remoteMarkerName, markerJSON}} {
		if err := remoteWriteFile(ctx, host, directory, file.name, file.data); err != nil {
			return fmt.Errorf("transfer %s: %w", file.name, err)
		}
	}
	if output, err := host.Run(ctx, remoteComposeCommand(candidate, "up --detach --pull always server"), nil); err != nil {
		if output != "" {
			return fmt.Errorf("start remote evaluation project: %w: %s", err, boundedCommandFailure(output))
		}
		return fmt.Errorf("start remote evaluation project: %w", err)
	}
	return nil
}

func remoteBoundaryAvailable(ctx context.Context, host remoteHost, candidate plan.DeploymentPlan) error {
	directory := candidate.Configuration["installDirectory"]
	if _, err := host.Run(ctx, "test ! -e "+shellQuote(directory), nil); err != nil {
		return errComposeInstallDirectoryExists
	}
	project := shellQuote(candidate.Configuration["projectName"])
	containers, err := host.Run(ctx, "docker ps --all --quiet --filter label=com.docker.compose.project="+project, nil)
	if err != nil || containers != "" {
		return errComposeContainersExist
	}
	volumes, err := host.Run(ctx, "docker volume ls --quiet --filter label=com.docker.compose.project="+project, nil)
	if err != nil || volumes != "" {
		return errComposeVolumesExist
	}
	return nil
}

func remoteOverride(project, containerName, volumeName, deploymentID, version string) string {
	return fmt.Sprintf(`services:
  server:
    container_name: %s
    labels:
      wranglerlabs.ranch-hand.managed: "true"
      wranglerlabs.ranch-hand.deployment: "%s"
      wranglerlabs.ranch-hand.version: "%s"
volumes:
  rw-data:
    name: %s
    labels:
      wranglerlabs.ranch-hand.managed: "true"
      wranglerlabs.ranch-hand.deployment: "%s"
      wranglerlabs.ranch-hand.version: "%s"
`, containerName, deploymentID, version, volumeName, deploymentID, version)
}

func remoteEnvironment(version string) string {
	return "BIND_ADDRESS=127.0.0.1\nPORT=8080\nDEMO_MODE=true\nAUTH_PROVIDERS=github\nENABLE_SCHEDULER=true\nAPP_VERSION=" + version + "\n"
}

func bytesSHA256(contents []byte) string {
	digest := sha256.Sum256(contents)
	return hex.EncodeToString(digest[:])
}

func remoteWriteFile(ctx context.Context, host remoteHost, directory, name string, contents []byte) error {
	destination := directory + "/" + name
	temporary := destination + ".ranch-hand-tmp"
	command := "umask 077; cat > " + shellQuote(temporary) + " && chmod 600 -- " + shellQuote(temporary) + " && mv -- " + shellQuote(temporary) + " " + shellQuote(destination)
	_, err := host.Run(ctx, command, contents)
	return err
}

func remoteComposeCommand(candidate plan.DeploymentPlan, action string) string {
	directory := candidate.Configuration["installDirectory"]
	// Compose interpolates every service before applying profiles. Ranch Hand's
	// SQLite evaluation does not start PostgreSQL, but the verified release
	// intentionally requires this variable for operators who enable that profile.
	// A process-only sentinel satisfies parsing without creating a database secret
	// or persisting a usable PostgreSQL credential in the target environment.
	return "POSTGRES_PASSWORD=ranch-hand-postgres-profile-disabled docker compose --project-name " + shellQuote(candidate.Configuration["projectName"]) +
		" --env-file " + shellQuote(directory+"/.env") + " --file " + shellQuote(directory+"/compose.yaml") +
		" --file " + shellQuote(directory+"/ranch-hand.override.yaml") + " " + action
}

func remoteComposeRecoveryCommand(candidate plan.DeploymentPlan) string {
	return remoteComposeCommand(candidate, "down --volumes --remove-orphans")
}

func (a *RemoteLinuxCompose) Verify(ctx context.Context, candidate plan.DeploymentPlan, credentials Credentials) error {
	host, err := a.connect(ctx, candidate, credentials)
	if err != nil {
		return err
	}
	defer host.Close()
	marker, err := readRemoteMarker(ctx, host, candidate)
	if err != nil {
		return err
	}
	if err := verifyRemoteFiles(ctx, host, candidate, marker); err != nil {
		return err
	}
	if err := verifyRemoteResources(ctx, host, marker); err != nil {
		return err
	}
	return verifyRemoteHealth(ctx, host, candidate.Release.Version)
}

func readRemoteMarker(ctx context.Context, host remoteHost, candidate plan.DeploymentPlan) (remoteInstallation, error) {
	directory := candidate.Configuration["installDirectory"]
	contents, err := host.Run(ctx, "cat -- "+shellQuote(directory+"/"+remoteMarkerName), nil)
	if err != nil {
		return remoteInstallation{}, errRemoteMarkerUnavailable
	}
	if contents == "" {
		return remoteInstallation{}, errRemoteMarkerEmpty
	}
	var marker remoteInstallation
	decoder := json.NewDecoder(strings.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&marker); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return remoteInstallation{}, errors.New("remote Ranch Hand ownership marker is invalid")
	}
	deploymentID, err := lifecycle.DeploymentID(candidate)
	if err != nil {
		return remoteInstallation{}, err
	}
	if marker.SchemaVersion != "1.0" || marker.DeploymentID != deploymentID || marker.Version != candidate.Release.Version || marker.ArtifactSHA256 != candidate.Release.ArtifactSHA256 || marker.ProjectName != candidate.Configuration["projectName"] || marker.ContainerName != marker.ProjectName+"-server" || marker.VolumeName != marker.ProjectName+"-data" || !remotePinnedImage(marker.Image) || !remoteDigestPattern.MatchString(marker.ComposeSHA256) || !remoteDigestPattern.MatchString(marker.OverrideSHA256) || !remoteDigestPattern.MatchString(marker.EnvironmentSHA256) {
		return remoteInstallation{}, errors.New("remote Ranch Hand ownership marker does not match this deployment")
	}
	return marker, nil
}

func remotePinnedImage(value string) bool {
	parts := strings.Split(value, "@sha256:")
	return len(parts) == 2 && parts[0] != "" && remoteDigestPattern.MatchString(parts[1])
}

func verifyRemoteFiles(ctx context.Context, host remoteHost, candidate plan.DeploymentPlan, marker remoteInstallation) error {
	directory := candidate.Configuration["installDirectory"]
	for name, expected := range map[string]string{"compose.yaml": marker.ComposeSHA256, "ranch-hand.override.yaml": marker.OverrideSHA256, ".env": marker.EnvironmentSHA256} {
		output, err := host.Run(ctx, "sha256sum -- "+shellQuote(directory+"/"+name), nil)
		if err != nil || len(output) < 64 || output[:64] != expected {
			return fmt.Errorf("remote deployment file %s no longer matches its ownership marker", name)
		}
	}
	return nil
}

func verifyRemoteResources(ctx context.Context, host remoteHost, marker remoteInstallation) error {
	labelsJSON, err := host.Run(ctx, "docker container inspect --format '{{json .Config.Labels}}' "+shellQuote(marker.ContainerName), nil)
	if err != nil {
		return errors.New("owned remote RepoWrangler container is unavailable")
	}
	if err := verifyRemoteLabels(labelsJSON, marker); err != nil {
		return err
	}
	image, err := host.Run(ctx, "docker container inspect --format '{{.Config.Image}}' "+shellQuote(marker.ContainerName), nil)
	if err != nil || image != marker.Image {
		return errors.New("remote container does not use the verified immutable image")
	}
	running, err := host.Run(ctx, "docker container inspect --format '{{.State.Running}}' "+shellQuote(marker.ContainerName), nil)
	if err != nil || running != "true" {
		return errors.New("remote RepoWrangler container is not running")
	}
	volumeLabels, err := host.Run(ctx, "docker volume inspect --format '{{json .Labels}}' "+shellQuote(marker.VolumeName), nil)
	if err != nil {
		return errors.New("owned remote RepoWrangler data volume is unavailable")
	}
	return verifyRemoteLabels(volumeLabels, marker)
}

func verifyRemoteLabels(contents string, marker remoteInstallation) error {
	var labels map[string]string
	if json.Unmarshal([]byte(contents), &labels) != nil || labels["wranglerlabs.ranch-hand.managed"] != "true" || labels["wranglerlabs.ranch-hand.deployment"] != marker.DeploymentID || labels["wranglerlabs.ranch-hand.version"] != marker.Version {
		return errors.New("remote Docker resource is not owned by this Ranch Hand deployment")
	}
	return nil
}

func verifyRemoteHealth(ctx context.Context, host remoteHost, version string) error {
	deadline, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		if remoteHealthReady(deadline, host, version) {
			return nil
		}
		select {
		case <-deadline.Done():
			return errors.New("remote RepoWrangler instance did not pass SSH-forwarded readiness and release-identity checks within five minutes")
		case <-ticker.C:
		}
	}
}

func remoteHealthReady(ctx context.Context, host remoteHost, version string) bool {
	for _, check := range []struct {
		path    string
		version bool
	}{{"/health/ready", false}, {"/health/live", true}} {
		status, body, err := host.Health(ctx, check.path)
		if err != nil || status != http.StatusOK {
			return false
		}
		var result struct {
			OK      bool   `json:"ok"`
			Version string `json:"version"`
		}
		if json.Unmarshal(body, &result) != nil || !result.OK || (check.version && result.Version != version) {
			return false
		}
	}
	return true
}

func (a *RemoteLinuxCompose) Recover(ctx context.Context, kind lifecycle.OperationKind, candidate plan.DeploymentPlan, _ string, backups lifecycle.OperationBackups, credentials Credentials) error {
	if kind != lifecycle.Install || backups.Selected != nil || backups.Safety != nil {
		return errors.New("remote Linux recovery currently supports only a failed new evaluation install")
	}
	host, err := a.connect(ctx, candidate, credentials)
	if err != nil {
		return err
	}
	defer host.Close()
	marker, err := readRemoteMarker(ctx, host, candidate)
	if err != nil {
		if (errors.Is(err, errRemoteMarkerUnavailable) || errors.Is(err, errRemoteMarkerEmpty)) && recoverRemoteBeforeMarker(ctx, host, candidate, errors.Is(err, errRemoteMarkerEmpty)) == nil {
			return nil
		}
		return errors.New("refusing remote cleanup without the exact Ranch Hand ownership marker")
	}
	return cleanupOwnedRemote(ctx, host, candidate, marker)
}

func (a *RemoteLinuxCompose) CleanupRemnant(ctx context.Context, candidate plan.DeploymentPlan, credentials Credentials) error {
	host, err := a.connect(ctx, candidate, credentials)
	if err != nil {
		return err
	}
	defer host.Close()
	marker, err := readRemoteMarker(ctx, host, candidate)
	if err == nil {
		return cleanupOwnedRemote(ctx, host, candidate, marker)
	}
	if errors.Is(err, errRemoteMarkerEmpty) {
		if recoverLegacyEmptyRemoteRemnant(ctx, host, candidate) == nil {
			return nil
		}
		return errors.New("refusing orphan cleanup because the legacy empty-marker directory contains unknown content or Docker resources")
	}
	if errors.Is(err, errRemoteMarkerUnavailable) {
		if err := verifyNoRemoteProjectResources(ctx, host, candidate); err != nil {
			return errors.New("refusing orphan cleanup because Docker resources exist for this project")
		}
		directory := candidate.Configuration["installDirectory"]
		command := "if [ ! -e " + shellQuote(directory) + " ]; then exit 0; fi; test -d " + shellQuote(directory) + " && test ! -L " + shellQuote(directory) + " && rmdir -- " + shellQuote(directory)
		if _, cleanupErr := host.Run(ctx, command, nil); cleanupErr == nil {
			return nil
		}
		return errors.New("refusing orphan cleanup without an exact ownership marker because the directory is not empty")
	}
	return errors.New("refusing orphan cleanup because the Ranch Hand ownership marker is invalid or does not match this deployment")
}

func recoverLegacyEmptyRemoteRemnant(ctx context.Context, host remoteHost, candidate plan.DeploymentPlan) error {
	if err := verifyNoRemoteProjectResources(ctx, host, candidate); err != nil {
		return err
	}
	directory := candidate.Configuration["installDirectory"]
	files := []string{"compose.yaml", "ranch-hand.override.yaml", ".env", remoteMarkerName}
	command := "test -d " + shellQuote(directory) + " && test ! -L " + shellQuote(directory)
	for _, name := range files {
		path := shellQuote(directory + "/" + name)
		command += " && test -f " + path + " && test ! -L " + path + " && test ! -s " + path
	}
	command += " && rm --force --"
	for _, name := range files {
		command += " " + shellQuote(directory+"/"+name)
	}
	command += " && rmdir -- " + shellQuote(directory)
	_, err := host.Run(ctx, command, nil)
	return err
}

func cleanupOwnedRemote(ctx context.Context, host remoteHost, candidate plan.DeploymentPlan, marker remoteInstallation) error {
	if err := verifyRemoteFiles(ctx, host, candidate, marker); err != nil {
		return errors.New("refusing remote cleanup because deployment files changed")
	}
	containerIDs, err := host.Run(ctx, "docker ps --all --quiet --filter "+shellQuote("name=^/"+marker.ContainerName+"$"), nil)
	if err != nil {
		return errors.New("failed-install remote container identity could not be inspected")
	}
	if containerIDs != "" {
		labels, inspectErr := host.Run(ctx, "docker container inspect --format '{{json .Config.Labels}}' "+shellQuote(marker.ContainerName), nil)
		if inspectErr != nil || verifyRemoteLabels(labels, marker) != nil {
			return errors.New("refusing to delete a remote container not owned by this Ranch Hand deployment")
		}
	}
	volumeIDs, err := host.Run(ctx, "docker volume ls --quiet --filter "+shellQuote("name=^"+marker.VolumeName+"$"), nil)
	if err != nil {
		return errors.New("failed-install remote volume identity could not be inspected")
	}
	if volumeIDs != "" {
		labels, inspectErr := host.Run(ctx, "docker volume inspect --format '{{json .Labels}}' "+shellQuote(marker.VolumeName), nil)
		if inspectErr != nil || verifyRemoteLabels(labels, marker) != nil {
			return errors.New("refusing to delete a remote volume not owned by this Ranch Hand deployment")
		}
	}
	if output, err := host.Run(ctx, remoteComposeRecoveryCommand(candidate), nil); err != nil {
		if output != "" {
			return fmt.Errorf("remove owned failed-install remote Compose project: %w: %s", err, boundedCommandFailure(output))
		}
		return fmt.Errorf("remove owned failed-install remote Compose project: %w", err)
	}
	directory := candidate.Configuration["installDirectory"]
	if _, err := host.Run(ctx, "rm --force -- "+shellQuote(directory+"/compose.yaml")+" "+shellQuote(directory+"/ranch-hand.override.yaml")+" "+shellQuote(directory+"/.env")+" "+shellQuote(directory+"/"+remoteMarkerName)+" && rmdir -- "+shellQuote(directory), nil); err != nil {
		return fmt.Errorf("remove owned failed-install remote directory: %w", err)
	}
	return nil
}

func recoverRemoteBeforeMarker(ctx context.Context, host remoteHost, candidate plan.DeploymentPlan, removeEmptyMarker bool) error {
	if err := verifyNoRemoteProjectResources(ctx, host, candidate); err != nil {
		return err
	}
	directory := candidate.Configuration["installDirectory"]
	markerCleanup := ""
	if removeEmptyMarker {
		marker := shellQuote(directory + "/" + remoteMarkerName)
		markerCleanup = "test -f " + marker + " && test ! -s " + marker + " && rm --force -- " + marker + " && "
	}
	command := "if [ ! -e " + shellQuote(directory) + " ]; then exit 0; fi; " +
		"test -d " + shellQuote(directory) + " && test ! -L " + shellQuote(directory) + " && " + markerCleanup + "rm --force -- " +
		shellQuote(directory+"/compose.yaml") + " " + shellQuote(directory+"/compose.yaml.ranch-hand-tmp") + " " +
		shellQuote(directory+"/ranch-hand.override.yaml") + " " + shellQuote(directory+"/ranch-hand.override.yaml.ranch-hand-tmp") + " " +
		shellQuote(directory+"/.env") + " " + shellQuote(directory+"/.env.ranch-hand-tmp") + " " +
		shellQuote(directory+"/"+remoteMarkerName+".ranch-hand-tmp") + " && rmdir -- " + shellQuote(directory)
	_, err := host.Run(ctx, command, nil)
	return err
}

func verifyNoRemoteProjectResources(ctx context.Context, host remoteHost, candidate plan.DeploymentPlan) error {
	project := candidate.Configuration["projectName"]
	containerIDs, err := host.Run(ctx, "docker ps --all --quiet --filter "+shellQuote("name=^/"+project+"-server$"), nil)
	if err != nil || containerIDs != "" {
		return errors.New("pre-marker recovery found a Compose container")
	}
	volumeIDs, err := host.Run(ctx, "docker volume ls --quiet --filter "+shellQuote("name=^"+project+"-data$"), nil)
	if err != nil || volumeIDs != "" {
		return errors.New("pre-marker recovery found a Compose volume")
	}
	return nil
}

func boundedCommandFailure(output string) string {
	output = strings.Map(func(character rune) rune {
		if character == '\n' || character == '\t' || character >= 0x20 {
			return character
		}
		return -1
	}, strings.TrimSpace(output))
	if len(output) > 4096 {
		return output[:4096] + "…"
	}
	return output
}
