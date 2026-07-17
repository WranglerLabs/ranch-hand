package adapter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/WranglerLabs/ranch-hand/internal/lifecycle"
)

func rollbackBackup(t *testing.T, candidateVersion, deploymentID, backupID string, createdAt time.Time) lifecycle.BackupRecord {
	t.Helper()
	record := lifecycle.BackupRecord{
		SchemaVersion: lifecycle.BackupSchemaVersion, BackupID: backupID, DeploymentID: deploymentID,
		OperationID: strings.Repeat("d", 32), Target: "local-compose", Version: candidateVersion, CreatedAt: createdAt,
		Artifact: lifecycle.BackupArtifact{Kind: lifecycle.LocalArchive, Locator: "backups/" + backupID + ".tar", Size: 42, SHA256: strings.Repeat("e", 64)},
	}
	if err := record.Validate(); err != nil {
		t.Fatal(err)
	}
	return record
}

func TestRollbackPoolPrunesOnlyVerifiedOlderEntries(t *testing.T) {
	candidate := localInstallPlan()
	deploymentID, err := lifecycle.DeploymentID(candidate)
	if err != nil {
		t.Fatal(err)
	}
	newer := rollbackBackup(t, "v1.2.3", deploymentID, strings.Repeat("a", 32), time.Now().UTC())
	older := rollbackBackup(t, "v1.2.2", deploymentID, strings.Repeat("b", 32), newer.CreatedAt.Add(-time.Hour))
	volumes := map[string]string{newer.BackupID: "new-volume", older.BackupID: "old-volume"}
	ids := map[string]string{newer.BackupID: "new-id", older.BackupID: "old-id"}
	var removedContainer, removedVolume bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, backup := range []lifecycle.BackupRecord{newer, older} {
			containerName := rollbackContainerName("repo-wrangler", backup.BackupID)
			if r.Method == http.MethodGet && r.URL.Path == "/containers/"+containerName+"/json" {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"Id": ids[backup.BackupID],
					"Config": map[string]any{"Labels": map[string]string{
						"com.wranglerlabs.ranch-hand.managed": "true", "com.wranglerlabs.ranch-hand.deployment": deploymentID,
						"com.wranglerlabs.ranch-hand.version": backup.Version,
					}},
					"State":  map[string]any{"Running": false},
					"Mounts": []map[string]string{{"Type": "volume", "Name": volumes[backup.BackupID], "Destination": "/app/data"}},
				})
				return
			}
			if r.Method == http.MethodGet && r.URL.Path == "/volumes/"+volumes[backup.BackupID] {
				_ = json.NewEncoder(w).Encode(map[string]any{"Labels": map[string]string{
					"com.wranglabs.invalid": "ignored", "com.wranglerlabs.ranch-hand.managed": "true", "com.wranglerlabs.ranch-hand.deployment": deploymentID,
				}})
				return
			}
		}
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/containers/old-id":
			if r.URL.Query().Get("force") != "0" || r.URL.Query().Get("v") != "0" {
				t.Fatal("rollback container pruning requested forced or implicit volume deletion")
			}
			removedContainer = true
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && r.URL.Path == "/volumes/old-volume":
			if !removedContainer {
				t.Fatal("rollback volume was removed before its stopped container")
			}
			removedVolume = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected Docker request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()
	manager := &LocalDocker{client: server.Client(), baseURL: server.URL}
	result, err := manager.PruneRollbackPool(context.Background(), candidate, []lifecycle.BackupRecord{older, newer}, 1)
	if err != nil || len(result.Kept) != 1 || result.Kept[0].BackupID != newer.BackupID || len(result.Pruned) != 1 || result.Pruned[0].BackupID != older.BackupID || !removedContainer || !removedVolume {
		t.Fatalf("rollback retention failed: %+v, %v, container=%t volume=%t", result, err, removedContainer, removedVolume)
	}
}

func TestRollbackPoolRefusesRunningPreservedContainer(t *testing.T) {
	candidate := localInstallPlan()
	deploymentID, _ := lifecycle.DeploymentID(candidate)
	backup := rollbackBackup(t, "v1.2.3", deploymentID, strings.Repeat("a", 32), time.Now().UTC())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"Id": "rollback-id", "Config": map[string]any{"Labels": map[string]string{
			"com.wranglerlabs.ranch-hand.managed": "true", "com.wranglerlabs.ranch-hand.deployment": deploymentID,
			"com.wranglerlabs.ranch-hand.version": backup.Version,
		}}, "State": map[string]any{"Running": true}, "Mounts": []map[string]string{{"Type": "volume", "Name": "data", "Destination": "/app/data"}}})
	}))
	defer server.Close()
	manager := &LocalDocker{client: server.Client(), baseURL: server.URL}
	if _, err := manager.RollbackPool(context.Background(), candidate, []lifecycle.BackupRecord{backup}); err == nil {
		t.Fatal("rollback pool accepted a running preserved container")
	}
}
