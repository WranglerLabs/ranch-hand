package adapter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/WranglerLabs/ranch-hand/internal/bundle"
	"github.com/WranglerLabs/ranch-hand/internal/lifecycle"
	"github.com/WranglerLabs/ranch-hand/internal/plan"
)

func localInstallPlan() plan.DeploymentPlan {
	return plan.DeploymentPlan{
		SchemaVersion: plan.CurrentSchemaVersion, Name: "Local Wrangler",
		Release: plan.ReleaseSelection{
			Version: "v1.2.3", ManifestURL: "https://github.com/WranglerLabs/repo-wrangler/releases/download/v1.2.3/release-manifest.json",
			ManifestSHA256: strings.Repeat("a", 64), ArtifactSHA256: strings.Repeat("b", 64), ArtifactSize: 42,
		},
		Target: plan.Target{Kind: "local-compose"},
		Configuration: map[string]string{
			"projectName": "repo-wrangler", "dataVolume": "repo-wrangler-data", "listenAddress": "127.0.0.1:18080",
		},
	}
}

func stagedComposeBundle(t *testing.T) bundle.StagedBundle {
	t.Helper()
	directory := t.TempDir()
	image := "ghcr.io/wranglerlabs/repo-wrangler-server@sha256:" + strings.Repeat("a", 64)
	postgres := "docker.io/library/postgres@sha256:" + strings.Repeat("b", 64)
	identity := `{"schemaVersion":"1.0","product":"RepoWrangler","version":"v1.2.3","targetFamily":"compose","image":"` + image + `","postgresImage":"` + postgres + `","publicHttps":"operator-provided","defaultBindAddress":"127.0.0.1"}`
	for name, contents := range map[string]string{"bundle.json": identity, "compose.yaml": "services: {}\n", ".env.example": "DEMO_MODE=true\n"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return bundle.StagedBundle{Product: "RepoWrangler", Version: "v1.2.3", Target: "local-compose", Path: directory}
}

func TestLocalDockerInstallUsesEngineAPI(t *testing.T) {
	var created map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/containers/repo-wrangler-server/json":
			http.Error(w, "missing", http.StatusNotFound)
		case r.Method == http.MethodPost && r.URL.Path == "/images/create":
			if !strings.Contains(r.URL.Query().Get("fromImage"), "@sha256:") {
				t.Fatal("image pull was not digest-pinned")
			}
			_, _ = io.WriteString(w, "{\"status\":\"done\"}\n")
		case r.Method == http.MethodGet && r.URL.Path == "/volumes/repo-wrangler-data":
			http.Error(w, "missing", http.StatusNotFound)
		case r.Method == http.MethodPost && r.URL.Path == "/volumes/create":
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"Name":"repo-wrangler-data","Labels":{"com.wranglerlabs.ranch-hand.managed":"true"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/containers/create":
			if r.URL.Query().Get("name") != "repo-wrangler-server" {
				t.Fatal("unexpected container name")
			}
			if err := json.NewDecoder(r.Body).Decode(&created); err != nil {
				t.Fatal(err)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"Id":"container-id"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/containers/container-id/start":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected Docker request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()
	adapter := &LocalDocker{client: server.Client(), baseURL: server.URL}
	err := adapter.Apply(context.Background(), lifecycle.Install, localInstallPlan(), stagedComposeBundle(t), nil, Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	if created["Image"] == "" {
		t.Fatal("container create request omitted image")
	}
	labels := created["Labels"].(map[string]any)
	if labels["com.wranglerlabs.ranch-hand.managed"] != "true" {
		t.Fatal("container create request omitted ownership label")
	}
	hostConfig := created["HostConfig"].(map[string]any)
	if len(hostConfig["Mounts"].([]any)) != 1 {
		t.Fatal("container create request omitted the persistent volume")
	}
}

func TestLocalDockerVerifyUsesLoopbackHealth(t *testing.T) {
	adapter := &LocalDocker{healthClient: localHealthClient(t, "v1.2.3")}
	if err := adapter.Verify(context.Background(), localInstallPlan(), Credentials{}); err != nil {
		t.Fatal(err)
	}
}

func TestLocalDockerHealthRejectsWrongReleaseIdentity(t *testing.T) {
	if localHealthReady(context.Background(), localHealthClient(t, "v1.2.2"), "v1.2.3") {
		t.Fatal("health verification accepted the wrong immutable release identity")
	}
}

func localHealthClient(t *testing.T, version string) *http.Client {
	t.Helper()
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		var body string
		switch r.URL.String() {
		case "http://127.0.0.1/health/ready":
			body = `{"ok":true,"demoMode":true}`
		case "http://127.0.0.1/health/live":
			body = `{"ok":true,"version":"` + version + `"}`
		default:
			t.Fatalf("unexpected health URL: %s", r.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
}

func TestLocalDockerRecoveryDeletesOnlyOwnedContainer(t *testing.T) {
	candidate := localInstallPlan()
	deploymentID, err := lifecycle.DeploymentID(candidate)
	if err != nil {
		t.Fatal(err)
	}
	deleted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "owned-id", "Config": map[string]any{"Labels": map[string]string{"com.wranglerlabs.ranch-hand.managed": "true", "com.wranglerlabs.ranch-hand.deployment": deploymentID}}})
		case http.MethodDelete:
			if r.URL.Path != "/containers/owned-id" {
				t.Fatal("recovery deleted unexpected container")
			}
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	adapter := &LocalDocker{client: server.Client(), baseURL: server.URL}
	if err := adapter.Recover(context.Background(), lifecycle.Install, candidate, nil, Credentials{}); err != nil {
		t.Fatal(err)
	}
	if !deleted {
		t.Fatal("owned partial container was not removed")
	}
}

func TestLocalDockerRecoveryRefusesUnownedContainer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"Id": "unowned", "Config": map[string]any{"Labels": map[string]string{}}})
	}))
	defer server.Close()
	adapter := &LocalDocker{client: server.Client(), baseURL: server.URL}
	if err := adapter.Recover(context.Background(), lifecycle.Install, localInstallPlan(), nil, Credentials{}); err == nil {
		t.Fatal("recovery removed or accepted an unowned container")
	}
}

func TestLocalDockerRejectsTrailingJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"Id":"first","Config":{"Labels":{}}}{"Id":"second"}`)
	}))
	defer server.Close()
	adapter := &LocalDocker{client: server.Client(), baseURL: server.URL}
	if _, _, err := adapter.containerMetadata(context.Background(), "repo-wrangler-server"); err == nil {
		t.Fatal("Docker response with trailing JSON was accepted")
	}
}

func TestLocalDockerBackupStopsArchivesRestartsAndVerifies(t *testing.T) {
	candidate := localInstallPlan()
	deploymentID, err := lifecycle.DeploymentID(candidate)
	if err != nil {
		t.Fatal(err)
	}
	labels := map[string]string{"com.wranglerlabs.ranch-hand.managed": "true", "com.wranglerlabs.ranch-hand.deployment": deploymentID, "com.wranglerlabs.ranch-hand.version": "v1.2.3"}
	var stopped, restarted, archived bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/containers/repo-wrangler-server/json":
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "owned-id", "Config": map[string]any{"Labels": labels}, "State": map[string]any{"Running": true}, "Mounts": []map[string]string{{"Type": "volume", "Name": "repo-wrangler-data", "Destination": "/app/data"}}})
		case r.Method == http.MethodGet && r.URL.Path == "/volumes/repo-wrangler-data":
			_ = json.NewEncoder(w).Encode(map[string]any{"Labels": labels})
		case r.Method == http.MethodPost && r.URL.Path == "/containers/owned-id/stop":
			stopped = true
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/containers/owned-id/archive":
			if !stopped || r.URL.Query().Get("path") != "/app/data" {
				t.Fatal("backup archive was requested without a consistent stop or fixed data path")
			}
			archived = true
			_, _ = io.WriteString(w, "deterministic-docker-tar")
		case r.Method == http.MethodPost && r.URL.Path == "/containers/owned-id/start":
			restarted = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected Docker request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()
	backupRoot := t.TempDir()
	adapter := &LocalDocker{
		client: server.Client(), baseURL: server.URL, backupRoot: backupRoot,
		healthClient: localHealthClient(t, "v1.2.3"),
	}
	artifact, err := adapter.Backup(context.Background(), candidate, Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	if !stopped || !archived || !restarted {
		t.Fatalf("backup lifecycle incomplete: stop=%t archive=%t restart=%t", stopped, archived, restarted)
	}
	if err := artifact.Validate(); err != nil {
		t.Fatal(err)
	}
	expectedDigest := sha256.Sum256([]byte("deterministic-docker-tar"))
	if artifact.SHA256 != hex.EncodeToString(expectedDigest[:]) || artifact.Size != int64(len("deterministic-docker-tar")) {
		t.Fatal("backup artifact integrity metadata does not match the archive")
	}
	contents, err := os.ReadFile(filepath.Join(backupRoot, filepath.FromSlash(artifact.Locator)))
	if err != nil || string(contents) != "deterministic-docker-tar" {
		t.Fatalf("backup archive was not committed: %v", err)
	}
}

func TestLocalDockerBackupRefusesUnownedVolumeBeforeStopping(t *testing.T) {
	candidate := localInstallPlan()
	deploymentID, err := lifecycle.DeploymentID(candidate)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/containers/repo-wrangler-server/json":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Id": "owned-id",
				"Config": map[string]any{"Labels": map[string]string{
					"com.wranglerlabs.ranch-hand.managed": "true", "com.wranglerlabs.ranch-hand.deployment": deploymentID, "com.wranglerlabs.ranch-hand.version": "v1.2.3",
				}},
				"State":  map[string]any{"Running": true},
				"Mounts": []map[string]string{{"Type": "volume", "Name": "repo-wrangler-data", "Destination": "/app/data"}},
			})
		case "/volumes/repo-wrangler-data":
			_ = json.NewEncoder(w).Encode(map[string]any{"Labels": map[string]string{}})
		default:
			t.Fatalf("backup mutated Docker after finding an unowned volume: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()
	adapter := &LocalDocker{client: server.Client(), baseURL: server.URL, backupRoot: t.TempDir()}
	if _, err := adapter.Backup(context.Background(), candidate, Credentials{}); err == nil {
		t.Fatal("backup accepted an unowned data volume")
	}
}

func localUpdateBackup(t *testing.T, candidate plan.DeploymentPlan, root string) lifecycle.BackupRecord {
	t.Helper()
	deploymentID, err := lifecycle.DeploymentID(candidate)
	if err != nil {
		t.Fatal(err)
	}
	contents := []byte("verified-update-backup")
	digest := sha256.Sum256(contents)
	token := strings.Repeat("d", 32)
	if err := os.MkdirAll(filepath.Join(root, "backups"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "backups", token+".tar"), contents, 0o600); err != nil {
		t.Fatal(err)
	}
	return lifecycle.BackupRecord{
		SchemaVersion: lifecycle.BackupSchemaVersion, BackupID: strings.Repeat("b", 32), DeploymentID: deploymentID,
		OperationID: strings.Repeat("c", 32), Target: "local-compose", Version: "v1.2.2", CreatedAt: time.Now().UTC(),
		Artifact: lifecycle.BackupArtifact{Kind: lifecycle.LocalArchive, Locator: "backups/" + token + ".tar", Size: int64(len(contents)), SHA256: hex.EncodeToString(digest[:])},
	}
}

func TestLocalDockerUpdateUsesCopyOnWriteVolume(t *testing.T) {
	candidate := localInstallPlan()
	backupRoot := t.TempDir()
	backup := localUpdateBackup(t, candidate, backupRoot)
	labels := map[string]string{
		"com.wranglerlabs.ranch-hand.managed": "true", "com.wranglerlabs.ranch-hand.deployment": backup.DeploymentID,
		"com.wranglerlabs.ranch-hand.version": "v1.2.2",
	}
	candidateVolume := updateVolumeName(backup.DeploymentID, backup.BackupID)
	var stopped, renamed, restored, started bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/containers/repo-wrangler-server/json":
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "old-id", "Config": map[string]any{"Labels": labels}, "State": map[string]any{"Running": true}, "Mounts": []map[string]string{{"Type": "volume", "Name": "old-volume", "Destination": "/app/data"}}})
		case r.Method == http.MethodGet && r.URL.Path == "/volumes/old-volume":
			_ = json.NewEncoder(w).Encode(map[string]any{"Labels": labels})
		case r.Method == http.MethodPost && r.URL.Path == "/images/create":
			_, _ = io.WriteString(w, "{\"status\":\"done\"}\n")
		case r.Method == http.MethodGet && r.URL.Path == "/volumes/"+candidateVolume:
			http.Error(w, "missing", http.StatusNotFound)
		case r.Method == http.MethodPost && r.URL.Path == "/volumes/create":
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"Labels":{}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/containers/old-id/stop":
			stopped = true
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/containers/old-id/rename":
			if r.URL.Query().Get("name") != rollbackContainerName("repo-wrangler", backup.BackupID) {
				t.Fatal("prior container was not assigned its deterministic rollback name")
			}
			renamed = true
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/containers/create":
			var payload struct {
				HostConfig struct {
					Mounts []struct{ Source string }
				}
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || len(payload.HostConfig.Mounts) != 1 || payload.HostConfig.Mounts[0].Source != candidateVolume {
				t.Fatal("updated container did not use the copy-on-write volume")
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"Id":"new-id"}`)
		case r.Method == http.MethodPut && r.URL.Path == "/containers/new-id/archive":
			contents, _ := io.ReadAll(r.Body)
			if r.URL.Query().Get("path") != "/app" || string(contents) != "verified-update-backup" {
				t.Fatal("verified backup was not restored into the candidate volume")
			}
			restored = true
		case r.Method == http.MethodPost && r.URL.Path == "/containers/new-id/start":
			started = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected update Docker request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()
	adapter := &LocalDocker{client: server.Client(), baseURL: server.URL, backupRoot: backupRoot}
	if err := adapter.Apply(context.Background(), lifecycle.Update, candidate, stagedComposeBundle(t), &backup, Credentials{}); err != nil {
		t.Fatal(err)
	}
	if !stopped || !renamed || !restored || !started {
		t.Fatalf("update sequence incomplete: stop=%t rename=%t restore=%t start=%t", stopped, renamed, restored, started)
	}
}

func TestLocalDockerUpdateRecoveryRestartsPreservedContainer(t *testing.T) {
	candidate := localInstallPlan()
	backup := localUpdateBackup(t, candidate, t.TempDir())
	newLabels := map[string]string{"com.wranglerlabs.ranch-hand.managed": "true", "com.wranglerlabs.ranch-hand.deployment": backup.DeploymentID, "com.wranglerlabs.ranch-hand.version": "v1.2.3"}
	oldLabels := map[string]string{"com.wranglerlabs.ranch-hand.managed": "true", "com.wranglerlabs.ranch-hand.deployment": backup.DeploymentID, "com.wranglerlabs.ranch-hand.version": "v1.2.2"}
	var removed, renamed, started bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/containers/repo-wrangler-server/json":
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "new-id", "Config": map[string]any{"Labels": newLabels}, "State": map[string]any{"Running": true}})
		case r.Method == http.MethodDelete && r.URL.Path == "/containers/new-id":
			removed = true
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/containers/repo-wrangler-rollback-"):
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "old-id", "Config": map[string]any{"Labels": oldLabels}, "State": map[string]any{"Running": false}})
		case r.Method == http.MethodPost && r.URL.Path == "/containers/old-id/rename":
			renamed = true
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/containers/old-id/start":
			started = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected recovery Docker request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()
	adapter := &LocalDocker{
		client: server.Client(), baseURL: server.URL,
		healthClient: localHealthClient(t, "v1.2.2"),
	}
	if err := adapter.Recover(context.Background(), lifecycle.Update, candidate, &backup, Credentials{}); err != nil {
		t.Fatal(err)
	}
	if !removed || !renamed || !started {
		t.Fatalf("update recovery incomplete: remove=%t rename=%t start=%t", removed, renamed, started)
	}
}
