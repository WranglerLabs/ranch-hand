package adapter

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/WranglerLabs/ranch-hand/internal/bundle"
	"github.com/WranglerLabs/ranch-hand/internal/lifecycle"
	"github.com/WranglerLabs/ranch-hand/internal/plan"
)

func remoteEvaluationPlan() plan.DeploymentPlan {
	return targetPlan("remote-linux-compose", map[string]string{
		"host": "192.168.1.165", "port": "22", "user": "repo-wrangler", "installDirectory": "/opt/repo-wrangler",
		"projectName": "repo-wrangler", "hostKeySha256": "SHA256:" + strings.Repeat("A", 43), "accessMode": "private-lan",
	})
}

func stagedRemoteBundle(t *testing.T) bundle.StagedBundle {
	t.Helper()
	directory := t.TempDir()
	image := repoWranglerV1010Companion.image
	postgres := "docker.io/library/postgres:16@sha256:" + strings.Repeat("b", 64)
	identity := `{"schemaVersion":"1.0","product":"RepoWrangler","version":"v1.2.3","targetFamily":"compose","image":"` + image + `","postgresImage":"` + postgres + `","publicHttps":"operator-provided","defaultBindAddress":"127.0.0.1"}`
	compose := "services:\n  server:\n    image: " + image + "\n    ports:\n      - \"${BIND_ADDRESS:-127.0.0.1}:${PORT:-8080}:8080\"\n    env_file: [.env]\n    volumes: [rw-data:/app/data]\nvolumes:\n  rw-data:\n"
	for name, contents := range map[string]string{"bundle.json": identity, "compose.yaml": compose, ".env.example": "DEMO_MODE=true\n"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return bundle.StagedBundle{Product: "RepoWrangler", Version: "v1.2.3", Target: "remote-linux-compose", Path: directory}
}

type fakeRemoteHost struct {
	files       map[string][]byte
	directory   bool
	resources   bool
	composeDown bool
	unowned     bool
	badImageID  bool
	commands    []string
	loadedImage []byte
}

func newFakeRemoteHost() *fakeRemoteHost { return &fakeRemoteHost{files: make(map[string][]byte)} }

func (h *fakeRemoteHost) Run(_ context.Context, command string, stdin []byte) (string, error) {
	h.commands = append(h.commands, command)
	switch {
	case command == "printenv HOME":
		return "/home/wsl", nil
	case command == "command -v docker":
		return "/usr/bin/docker", nil
	case strings.HasPrefix(command, "docker version --format"):
		return "29.0.0/linux/amd64", nil
	case command == "docker compose version --short":
		return "2.40.0", nil
	case strings.HasPrefix(command, "docker image inspect --format '{{.Id}}' "):
		return repoWranglerV1010Companion.imageID, nil
	case strings.HasPrefix(command, "test ! -e "):
		if h.directory {
			return "", errors.New("exists")
		}
		return "", nil
	case strings.HasPrefix(command, "mkdir --mode=700"):
		h.directory = true
		return "", nil
	case strings.HasPrefix(command, "umask 077; cat > "):
		for _, name := range []string{"compose.yaml", "ranch-hand.override.yaml", ".env", remoteMarkerName} {
			if strings.Contains(command, "/"+name+".ranch-hand-tmp") {
				h.files[name] = append([]byte(nil), stdin...)
				return "", nil
			}
		}
		return "", errors.New("unknown transfer")
	case strings.Contains(command, "docker compose") && strings.HasSuffix(command, "up --detach --pull never server"):
		if !strings.HasPrefix(command, "POSTGRES_PASSWORD=ranch-hand-postgres-profile-disabled ") || !strings.Contains(command, " --env-file ") {
			return "required variable POSTGRES_PASSWORD is missing a value", errors.New("compose interpolation failed")
		}
		h.resources = true
		return "started", nil
	case strings.Contains(command, "docker compose") && strings.HasSuffix(command, "down --volumes --remove-orphans"):
		if !strings.HasPrefix(command, "POSTGRES_PASSWORD=ranch-hand-postgres-profile-disabled ") {
			return "required variable POSTGRES_PASSWORD is missing a value", errors.New("compose interpolation failed")
		}
		h.resources = false
		h.composeDown = true
		return "removed", nil
	case strings.HasPrefix(command, "cat -- "):
		contents, ok := h.files[remoteMarkerName]
		if !ok {
			return "", errors.New("missing")
		}
		return string(contents), nil
	case strings.HasPrefix(command, "if [ ! -e ") && !strings.Contains(command, "rm --force"):
		if !h.directory {
			return "", nil
		}
		if len(h.files) != 0 {
			return "", errors.New("directory not empty")
		}
		h.directory = false
		return "", nil
	case strings.HasPrefix(command, "if [ ! -e "):
		if !h.directory {
			return "", nil
		}
		for name := range h.files {
			known := name == "compose.yaml" || name == "compose.yaml.ranch-hand-tmp" || name == "ranch-hand.override.yaml" || name == "ranch-hand.override.yaml.ranch-hand-tmp" || name == ".env" || name == ".env.ranch-hand-tmp" || name == remoteMarkerName+".ranch-hand-tmp" || (name == remoteMarkerName && len(h.files[name]) == 0 && strings.Contains(command, "test ! -s"))
			if !known {
				return "", errors.New("directory contains unknown content")
			}
		}
		h.files = make(map[string][]byte)
		h.directory = false
		return "", nil
	case strings.HasPrefix(command, "test -d ") && strings.Contains(command, "test ! -s"):
		expected := map[string]bool{"compose.yaml": true, "ranch-hand.override.yaml": true, ".env": true, remoteMarkerName: true}
		if len(h.files) != len(expected) {
			return "", errors.New("legacy directory contains unknown content")
		}
		for name := range expected {
			contents, ok := h.files[name]
			if !ok || len(contents) != 0 {
				return "", errors.New("legacy file is missing or non-empty")
			}
		}
		h.files = make(map[string][]byte)
		h.directory = false
		return "", nil
	case strings.HasPrefix(command, "sha256sum -- "):
		for _, name := range []string{"compose.yaml", "ranch-hand.override.yaml", ".env"} {
			if strings.Contains(command, "/"+name+"'") {
				return bytesSHA256(h.files[name]) + "  " + name, nil
			}
		}
		return "", errors.New("missing file")
	case strings.Contains(command, "docker ps --all --quiet --filter label="):
		if h.resources {
			return "container-id", nil
		}
		return "", nil
	case strings.Contains(command, "docker volume ls --quiet --filter label="):
		if h.resources {
			return "repo-wrangler-data", nil
		}
		return "", nil
	case strings.Contains(command, "docker ps --all --quiet --filter") && strings.Contains(command, "name="):
		if h.resources {
			return "container-id", nil
		}
		return "", nil
	case strings.Contains(command, "docker volume ls --quiet --filter") && strings.Contains(command, "name="):
		if h.resources {
			return "repo-wrangler-data", nil
		}
		return "", nil
	case strings.Contains(command, "inspect --format '{{json .Config.Labels}}'") || strings.Contains(command, "inspect --format '{{json .Labels}}'"):
		marker := h.marker()
		deploymentID := marker.DeploymentID
		if h.unowned {
			deploymentID = "someone-else"
		}
		encoded, _ := json.Marshal(map[string]string{
			"wranglerlabs.ranch-hand.managed": "true", "wranglerlabs.ranch-hand.deployment": deploymentID,
			"wranglerlabs.ranch-hand.version": marker.Version,
		})
		return string(encoded), nil
	case strings.Contains(command, "inspect --format '{{.Config.Image}}'"):
		marker := h.marker()
		if marker.RuntimeImage != "" {
			return marker.RuntimeImage, nil
		}
		return marker.Image, nil
	case strings.Contains(command, "inspect --format '{{.Image}}'"):
		if h.badImageID {
			return "sha256:" + strings.Repeat("f", 64), nil
		}
		return repoWranglerV1010Companion.imageID, nil
	case strings.Contains(command, "inspect --format '{{.State.Running}}'"):
		return "true", nil
	case strings.HasPrefix(command, "rm --force -- "):
		h.directory = false
		h.files = make(map[string][]byte)
		return "", nil
	default:
		return "", errors.New("unexpected command: " + command)
	}
}

func (h *fakeRemoteHost) LoadImage(_ context.Context, source io.Reader) (string, error) {
	contents, err := io.ReadAll(source)
	if err != nil {
		return "", err
	}
	h.loadedImage = contents
	return "Loaded image: " + repoWranglerV1010Companion.runtimeImage, nil
}

func (h *fakeRemoteHost) marker() remoteInstallation {
	var marker remoteInstallation
	_ = json.Unmarshal(h.files[remoteMarkerName], &marker)
	return marker
}

func (h *fakeRemoteHost) Health(_ context.Context, path string) (int, []byte, error) {
	if path == "/health/live" {
		return http.StatusOK, []byte(`{"ok":true,"version":"v1.2.3"}`), nil
	}
	if path == "/" {
		return http.StatusOK, []byte(`<!doctype html><div id="root"></div><script type="module" src="/assets/index-test.js"></script>`), nil
	}
	if path == "/assets/index-test.js" {
		return http.StatusOK, bytes.Repeat([]byte("x"), 1024), nil
	}
	return http.StatusOK, []byte(`{"ok":true}`), nil
}

func (h *fakeRemoteHost) Close() error { return nil }

func TestRemoteEvaluationInstallTransfersVerifiedBundleAndChecksIdentity(t *testing.T) {
	host := newFakeRemoteHost()
	adapter := newRemoteLinuxCompose(func(context.Context, plan.DeploymentPlan, Credentials) (remoteHost, error) { return host, nil })
	candidate := remoteEvaluationPlan()
	credentials := Credentials{SSHPassword: "runtime-only"}
	if err := adapter.Apply(context.Background(), lifecycle.Install, candidate, "", stagedRemoteBundle(t), lifecycle.OperationBackups{}, credentials); err != nil {
		t.Fatal(err)
	}
	if !host.directory || !host.resources || len(host.files) != 4 {
		t.Fatal("remote install did not create its dedicated files and Docker resources")
	}
	environment := string(host.files[".env"])
	if strings.Contains(environment, credentials.SSHPassword) || !strings.Contains(environment, "BIND_ADDRESS=0.0.0.0") || !strings.Contains(environment, "PUBLIC_BASE_URL=http://192.168.1.165:8080") || strings.Contains(environment, "POSTGRES_PASSWORD=") {
		t.Fatal("remote evaluation environment exposed a secret or failed to bind the selected private network")
	}
	directVerified := false
	adapter.verifyRemoteAccess = func(_ context.Context, verified plan.DeploymentPlan, version string) error {
		directVerified = verified.Configuration["host"] == "192.168.1.165" && version == "v1.2.3"
		return nil
	}
	if err := adapter.Verify(context.Background(), candidate, credentials); err != nil {
		t.Fatal(err)
	}
	if !directVerified {
		t.Fatal("remote install did not verify the private-network endpoint")
	}
}

func TestRemoteRealModeGeneratesRequiredSetupSecrets(t *testing.T) {
	host := newFakeRemoteHost()
	adapter := newRemoteLinuxCompose(func(context.Context, plan.DeploymentPlan, Credentials) (remoteHost, error) { return host, nil })
	candidate := remoteEvaluationPlan()
	candidate.Configuration["demoMode"] = "false"
	if err := adapter.Apply(context.Background(), lifecycle.Install, candidate, "", stagedRemoteBundle(t), lifecycle.OperationBackups{}, Credentials{SSHPassword: "runtime-only", SetupToken: "abcdefghijklmnopqrstuvwxyz012345"}); err != nil {
		t.Fatal(err)
	}
	environment := string(host.files[".env"])
	for _, expected := range []string{"DEMO_MODE=false", "BIND_ADDRESS=0.0.0.0", "PUBLIC_BASE_URL=http://192.168.1.165:8080", "SESSION_SECRET=", "SECRET_ENCRYPTION_KEY=", "SETUP_TOKEN=abcdefghijklmnopqrstuvwxyz012345"} {
		if !strings.Contains(environment, expected) {
			t.Fatalf("real mode environment is missing %s", expected)
		}
	}
	if strings.Contains(environment, "change-me") || strings.Contains(environment, "runtime-only") {
		t.Fatal("real mode environment used a placeholder or exposed runtime credentials")
	}
}

func TestRemoteRealModeRefusesUnprotectedInitialSetup(t *testing.T) {
	host := newFakeRemoteHost()
	adapter := newRemoteLinuxCompose(func(context.Context, plan.DeploymentPlan, Credentials) (remoteHost, error) { return host, nil })
	candidate := remoteEvaluationPlan()
	candidate.Configuration["demoMode"] = "false"
	err := adapter.Apply(context.Background(), lifecycle.Install, candidate, "", stagedRemoteBundle(t), lifecycle.OperationBackups{}, Credentials{SSHPassword: "runtime-only"})
	if err == nil || !strings.Contains(err.Error(), "setup token") {
		t.Fatalf("expected real remote install without setup protection to fail, got %v", err)
	}
	if len(host.files) != 0 {
		t.Fatal("remote install mutated the target before rejecting the missing setup token")
	}
}

func TestWSLEnvironmentRemainsLoopbackOnly(t *testing.T) {
	candidate := remoteEvaluationPlan()
	candidate.Target.Kind = "local-wsl-compose"
	candidate.Configuration = map[string]string{"distribution": "Ubuntu-26.04", "projectName": "repo-wrangler", "demoMode": "false"}
	environment, err := remoteEnvironment(candidate, Credentials{}, false)
	if err != nil {
		t.Fatal(err)
	}
	contents := string(environment)
	if !strings.Contains(contents, "BIND_ADDRESS=127.0.0.1") || !strings.Contains(contents, "PUBLIC_BASE_URL=http://127.0.0.1:8080") || strings.Contains(contents, "BIND_ADDRESS=0.0.0.0") {
		t.Fatalf("local WSL environment crossed the remote network boundary: %s", contents)
	}
}

func TestRemoteEvaluationInstallStreamsVerifiedImageWithoutRegistryPull(t *testing.T) {
	t.Setenv("LocalAppData", t.TempDir())
	archive := []byte("small verified Docker archive fixture")
	digest := sha256.Sum256(archive)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusOK)
		_, _ = response.Write(archive)
	}))
	defer server.Close()

	original := repoWranglerV1010Companion
	repoWranglerV1010Companion = companionImage{
		image:        "ghcr.io/wranglerlabs/repo-wrangler-server@sha256:" + strings.Repeat("a", 64),
		runtimeImage: "repo-wrangler-server:test-verified-archive",
		imageID:      "sha256:" + strings.Repeat("a", 64),
		url:          server.URL + "/repo-wrangler-test-image.tar.gz",
		sha256:       fmt.Sprintf("%x", digest),
		size:         int64(len(archive)),
	}
	t.Cleanup(func() { repoWranglerV1010Companion = original })

	host := newFakeRemoteHost()
	adapter := newRemoteLinuxCompose(func(context.Context, plan.DeploymentPlan, Credentials) (remoteHost, error) { return host, nil })
	adapter.prepareReleaseImage = adapter.prepareRemoteCompanion
	if err := adapter.Apply(context.Background(), lifecycle.Install, remoteEvaluationPlan(), "", stagedRemoteBundle(t), lifecycle.OperationBackups{}, Credentials{SSHPassword: "runtime-only"}); err != nil {
		t.Fatal(err)
	}
	if string(host.loadedImage) != string(archive) {
		t.Fatal("verified image archive was not streamed to the remote Docker Engine")
	}
	for _, command := range host.commands {
		if strings.HasPrefix(command, "docker image pull ") {
			t.Fatalf("remote archive install contacted a registry: %s", command)
		}
	}
	override := string(host.files["ranch-hand.override.yaml"])
	if !strings.Contains(override, "pull_policy: never") || !strings.Contains(override, repoWranglerV1010Companion.runtimeImage) {
		t.Fatalf("remote Compose was not pinned to the loaded image: %s", override)
	}
}

func TestWSLStyleInstallUsesVerifiedLoadedImageWithoutRegistryPull(t *testing.T) {
	host := newFakeRemoteHost()
	adapter := newRemoteLinuxCompose(func(context.Context, plan.DeploymentPlan, Credentials) (remoteHost, error) { return host, nil })
	candidate := remoteEvaluationPlan()
	staged := stagedRemoteBundle(t)
	for _, name := range []string{"bundle.json", "compose.yaml"} {
		path := filepath.Join(staged.Path, name)
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		contents = []byte(strings.ReplaceAll(string(contents), "ghcr.io/wranglerlabs/repo-wrangler-server@sha256:"+strings.Repeat("a", 64), repoWranglerV1010Companion.image))
		if err := os.WriteFile(path, contents, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := adapter.apply(context.Background(), lifecycle.Install, candidate, staged, lifecycle.OperationBackups{}, Credentials{}, repoWranglerV1010Companion.runtimeImage, false); err != nil {
		t.Fatal(err)
	}
	for _, command := range host.commands {
		if strings.HasPrefix(command, "docker image pull ") {
			t.Fatalf("loaded-image install contacted a registry: %s", command)
		}
	}
	override := string(host.files["ranch-hand.override.yaml"])
	if !strings.Contains(override, "image: "+repoWranglerV1010Companion.runtimeImage) || !strings.Contains(override, "pull_policy: never") {
		t.Fatalf("loaded image was not fixed in the Compose override: %s", override)
	}
	marker := host.marker()
	if marker.SchemaVersion != "1.1" || marker.RuntimeImage != repoWranglerV1010Companion.runtimeImage {
		t.Fatalf("loaded image identity was not recorded: %+v", marker)
	}
	if err := adapter.Verify(context.Background(), candidate, Credentials{}); err != nil {
		t.Fatal(err)
	}
}

func TestWSLStyleVerificationRejectsRepointedRuntimeTag(t *testing.T) {
	host := newFakeRemoteHost()
	adapter := newRemoteLinuxCompose(func(context.Context, plan.DeploymentPlan, Credentials) (remoteHost, error) { return host, nil })
	candidate := remoteEvaluationPlan()
	staged := stagedRemoteBundle(t)
	for _, name := range []string{"bundle.json", "compose.yaml"} {
		path := filepath.Join(staged.Path, name)
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		contents = []byte(strings.ReplaceAll(string(contents), "ghcr.io/wranglerlabs/repo-wrangler-server@sha256:"+strings.Repeat("a", 64), repoWranglerV1010Companion.image))
		if err := os.WriteFile(path, contents, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := adapter.apply(context.Background(), lifecycle.Install, candidate, staged, lifecycle.OperationBackups{}, Credentials{}, repoWranglerV1010Companion.runtimeImage, false); err != nil {
		t.Fatal(err)
	}
	host.badImageID = true
	if err := adapter.Verify(context.Background(), candidate, Credentials{}); err == nil || !strings.Contains(err.Error(), "runtime image ID") {
		t.Fatalf("repointed runtime tag was accepted: %v", err)
	}
}

func TestRemotePreflightRequiresPinnedUnusedBoundary(t *testing.T) {
	host := newFakeRemoteHost()
	adapter := newRemoteLinuxCompose(func(context.Context, plan.DeploymentPlan, Credentials) (remoteHost, error) { return host, nil })
	report := adapter.Preflight(context.Background(), remoteEvaluationPlan(), Credentials{SSHPassword: "runtime-only"})
	if !report.Ready {
		t.Fatalf("remote evaluation preflight was not ready: %+v", report)
	}
}

func TestRemoteRecoveryRemovesOnlyMarkerOwnedResources(t *testing.T) {
	host := newFakeRemoteHost()
	adapter := newRemoteLinuxCompose(func(context.Context, plan.DeploymentPlan, Credentials) (remoteHost, error) { return host, nil })
	candidate := remoteEvaluationPlan()
	credentials := Credentials{SSHPassword: "runtime-only"}
	if err := adapter.Apply(context.Background(), lifecycle.Install, candidate, "", stagedRemoteBundle(t), lifecycle.OperationBackups{}, credentials); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Recover(context.Background(), lifecycle.Install, candidate, "", lifecycle.OperationBackups{}, credentials); err != nil {
		t.Fatal(err)
	}
	if !host.composeDown || host.directory || host.resources {
		t.Fatal("owned failed-install remote project was not removed")
	}
}

func TestRemoteUninstallRemovesOnlyMarkerOwnedResources(t *testing.T) {
	host := newFakeRemoteHost()
	adapter := newRemoteLinuxCompose(func(context.Context, plan.DeploymentPlan, Credentials) (remoteHost, error) { return host, nil })
	candidate := remoteEvaluationPlan()
	credentials := Credentials{SSHPassword: "runtime-only"}
	if err := adapter.Apply(context.Background(), lifecycle.Install, candidate, "", stagedRemoteBundle(t), lifecycle.OperationBackups{}, credentials); err != nil {
		t.Fatal(err)
	}
	if err := adapter.Apply(context.Background(), lifecycle.Uninstall, candidate, candidate.Release.Version, bundle.StagedBundle{}, lifecycle.OperationBackups{}, credentials); err != nil {
		t.Fatal(err)
	}
	if !host.composeDown || host.directory || host.resources {
		t.Fatal("owned remote deployment was not uninstalled")
	}
}

func TestRemoteRemnantCleanupRequiresMarkerOrEmptyDirectory(t *testing.T) {
	candidate := remoteEvaluationPlan()
	credentials := Credentials{SSHPassword: "runtime-only"}

	owned := newFakeRemoteHost()
	ownedAdapter := newRemoteLinuxCompose(func(context.Context, plan.DeploymentPlan, Credentials) (remoteHost, error) { return owned, nil })
	if err := ownedAdapter.Apply(context.Background(), lifecycle.Install, candidate, "", stagedRemoteBundle(t), lifecycle.OperationBackups{}, credentials); err != nil {
		t.Fatal(err)
	}
	if err := ownedAdapter.CleanupRemnant(context.Background(), candidate, credentials); err != nil {
		t.Fatal(err)
	}
	if owned.directory || owned.resources || !owned.composeDown {
		t.Fatal("marker-owned remnant was not removed")
	}

	empty := newFakeRemoteHost()
	empty.directory = true
	emptyAdapter := newRemoteLinuxCompose(func(context.Context, plan.DeploymentPlan, Credentials) (remoteHost, error) { return empty, nil })
	if err := emptyAdapter.CleanupRemnant(context.Background(), candidate, credentials); err != nil || empty.directory {
		t.Fatalf("empty dedicated directory was not removed: %v", err)
	}

	legacy := newFakeRemoteHost()
	legacy.directory = true
	for _, name := range []string{"compose.yaml", "ranch-hand.override.yaml", ".env", remoteMarkerName} {
		legacy.files[name] = []byte{}
	}
	legacyAdapter := newRemoteLinuxCompose(func(context.Context, plan.DeploymentPlan, Credentials) (remoteHost, error) { return legacy, nil })
	if err := legacyAdapter.CleanupRemnant(context.Background(), candidate, credentials); err != nil || legacy.directory {
		t.Fatalf("exact legacy empty-marker remnant was not removed: %v", err)
	}

	legacy.directory = true
	legacy.files = map[string][]byte{"compose.yaml": []byte("changed"), "ranch-hand.override.yaml": {}, ".env": {}, remoteMarkerName: {}}
	if err := legacyAdapter.CleanupRemnant(context.Background(), candidate, credentials); err == nil || !legacy.directory {
		t.Fatal("non-empty legacy marker pattern was removed or accepted")
	}

	unknown := newFakeRemoteHost()
	unknown.directory = true
	unknown.files["keep-me"] = []byte("not Ranch Hand content")
	unknownAdapter := newRemoteLinuxCompose(func(context.Context, plan.DeploymentPlan, Credentials) (remoteHost, error) { return unknown, nil })
	if err := unknownAdapter.CleanupRemnant(context.Background(), candidate, credentials); err == nil || !unknown.directory || len(unknown.files) != 1 {
		t.Fatal("unknown markerless directory was removed or accepted")
	}
}

func TestRemoteRecoveryRemovesPreviewInstallWhoseEnvironmentLacksPostgresPassword(t *testing.T) {
	host := newFakeRemoteHost()
	adapter := newRemoteLinuxCompose(func(context.Context, plan.DeploymentPlan, Credentials) (remoteHost, error) { return host, nil })
	candidate := remoteEvaluationPlan()
	credentials := Credentials{SSHPassword: "runtime-only"}
	if err := adapter.Apply(context.Background(), lifecycle.Install, candidate, "", stagedRemoteBundle(t), lifecycle.OperationBackups{}, credentials); err != nil {
		t.Fatal(err)
	}

	// rc.9 and earlier wrote this environment. Its marker legitimately hashes
	// the old contents, but Compose cannot interpolate the profiled PostgreSQL
	// service unless recovery supplies the missing value independently.
	host.files[".env"] = []byte("BIND_ADDRESS=127.0.0.1\nPORT=8080\nDEMO_MODE=true\nAUTH_PROVIDERS=github\nENABLE_SCHEDULER=true\nAPP_VERSION=v1.2.3\n")
	marker := host.marker()
	marker.EnvironmentSHA256 = bytesSHA256(host.files[".env"])
	encoded, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	host.files[remoteMarkerName] = append(encoded, '\n')

	if err := adapter.Recover(context.Background(), lifecycle.Install, candidate, "", lifecycle.OperationBackups{}, credentials); err != nil {
		t.Fatal(err)
	}
	if !host.composeDown || host.directory || host.resources {
		t.Fatal("preview environment without POSTGRES_PASSWORD was not recovered")
	}
}

func TestRemoteRecoveryRefusesUnownedContainer(t *testing.T) {
	host := newFakeRemoteHost()
	adapter := newRemoteLinuxCompose(func(context.Context, plan.DeploymentPlan, Credentials) (remoteHost, error) { return host, nil })
	candidate := remoteEvaluationPlan()
	credentials := Credentials{SSHPassword: "runtime-only"}
	if err := adapter.Apply(context.Background(), lifecycle.Install, candidate, "", stagedRemoteBundle(t), lifecycle.OperationBackups{}, credentials); err != nil {
		t.Fatal(err)
	}
	host.unowned = true
	if err := adapter.Recover(context.Background(), lifecycle.Install, candidate, "", lifecycle.OperationBackups{}, credentials); err == nil || host.composeDown {
		t.Fatal("remote recovery deleted or accepted an unowned container")
	}
}

func TestRemoteRecoveryRemovesOnlyEmptyPreMarkerDirectory(t *testing.T) {
	host := newFakeRemoteHost()
	host.directory = true
	adapter := newRemoteLinuxCompose(func(context.Context, plan.DeploymentPlan, Credentials) (remoteHost, error) { return host, nil })
	if err := adapter.Recover(context.Background(), lifecycle.Install, remoteEvaluationPlan(), "", lifecycle.OperationBackups{}, Credentials{SSHPassword: "runtime-only"}); err != nil {
		t.Fatal(err)
	}
	if host.directory || host.composeDown {
		t.Fatal("pre-marker recovery did not remove only the empty directory")
	}

	host.directory = true
	host.files["compose.yaml"] = []byte("partial Ranch Hand transfer")
	host.files[".env.ranch-hand-tmp"] = []byte("partial Ranch Hand transfer")
	if err := adapter.Recover(context.Background(), lifecycle.Install, remoteEvaluationPlan(), "", lifecycle.OperationBackups{}, Credentials{SSHPassword: "runtime-only"}); err != nil {
		t.Fatal(err)
	}
	if host.directory || len(host.files) != 0 {
		t.Fatal("pre-marker recovery did not remove the fixed partial-transfer files")
	}

	host.directory = true
	host.files["compose.yaml"] = []byte{}
	host.files["ranch-hand.override.yaml"] = []byte{}
	host.files[".env"] = []byte{}
	host.files[remoteMarkerName] = []byte{}
	if err := adapter.Recover(context.Background(), lifecycle.Install, remoteEvaluationPlan(), "", lifecycle.OperationBackups{}, Credentials{SSHPassword: "runtime-only"}); err != nil {
		t.Fatal(err)
	}
	if host.directory || len(host.files) != 0 {
		t.Fatal("legacy empty-marker recovery did not remove the exact empty transfer files")
	}

	host.directory = true
	host.files["unknown"] = []byte("do not remove")
	if err := adapter.Recover(context.Background(), lifecycle.Install, remoteEvaluationPlan(), "", lifecycle.OperationBackups{}, Credentials{SSHPassword: "runtime-only"}); err == nil || !host.directory || len(host.files) != 1 {
		t.Fatal("pre-marker recovery removed or accepted a non-empty directory")
	}

	host.files = make(map[string][]byte)
	host.resources = true
	if err := adapter.Recover(context.Background(), lifecycle.Install, remoteEvaluationPlan(), "", lifecycle.OperationBackups{}, Credentials{SSHPassword: "runtime-only"}); err == nil || !host.directory || !host.resources {
		t.Fatal("pre-marker recovery accepted a project with Docker resources")
	}
}

func TestRemoteShellScriptPipesPayloadToWholeCompoundCommand(t *testing.T) {
	script := remoteShellScript("umask 077; cat > '/tmp/compose.yaml'", []byte("services:\n"))
	if !strings.Contains(script, "| ( umask 077; cat > '/tmp/compose.yaml' )") {
		t.Fatalf("payload was not piped to the compound command subshell: %s", script)
	}
}
