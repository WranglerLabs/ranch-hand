package lifecycle

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/WranglerLabs/ranch-hand/internal/plan"
)

func lifecyclePlan(version string) plan.DeploymentPlan {
	return plan.DeploymentPlan{
		SchemaVersion: plan.CurrentSchemaVersion,
		Name:          "Local RepoWrangler",
		Release: plan.ReleaseSelection{
			Version: version, ManifestURL: "https://github.com/WranglerLabs/repo-wrangler/releases/download/" + version + "/release-manifest.json",
			ManifestSHA256: strings.Repeat("a", 64), ArtifactSHA256: strings.Repeat("b", 64), ArtifactSize: 42,
		},
		Target: plan.Target{Kind: "local-compose"},
		Configuration: map[string]string{
			"projectName": "repo-wrangler", "dataDirectory": `C:\RepoWrangler\data`, "listenAddress": "127.0.0.1:8080",
		},
	}
}

func testStore(t *testing.T) *Store {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 17, 4, 0, 0, 0, time.UTC)
	store.now = func() time.Time {
		now = now.Add(time.Second)
		return now
	}
	return store
}

func transition(t *testing.T, store *Store, journal Journal, phases ...Phase) Journal {
	t.Helper()
	var err error
	for _, phase := range phases {
		journal, err = store.Transition(journal.DeploymentID, journal.OperationID, phase)
		if err != nil {
			t.Fatalf("transition to %s failed: %v", phase, err)
		}
	}
	return journal
}

func TestInstallJournalCommitsAndReleasesLock(t *testing.T) {
	store := testStore(t)
	journal, err := store.Begin(Install, lifecyclePlan("v1.2.3"), "")
	if err != nil {
		t.Fatal(err)
	}
	journal = transition(t, store, journal, Staged, Applied, Verified, Committed)
	if journal.Phase != Committed || len(journal.Events) != 5 {
		t.Fatalf("unexpected committed journal: %+v", journal)
	}
	if _, err := store.Active(journal.DeploymentID); !os.IsNotExist(err) {
		t.Fatalf("terminal operation retained active lock: %v", err)
	}
	planPath := filepath.Join(store.deploymentDirectory(journal.DeploymentID), "operations", journal.OperationID+".plan.json")
	if _, err := os.Stat(planPath); err != nil {
		t.Fatalf("operation plan snapshot missing: %v", err)
	}
}

func TestUpdateRequiresBackupBeforeCommit(t *testing.T) {
	store := testStore(t)
	journal, err := store.Begin(Update, lifecyclePlan("v1.2.4"), "v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	journal = transition(t, store, journal, Staged, Applied, Verified)
	if _, err := store.Transition(journal.DeploymentID, journal.OperationID, Committed); err == nil {
		t.Fatal("update committed without a backup")
	}
	transition(t, store, journal, RecoveryStarted, Recovered)
}

func TestBackupFirstUpdateCommits(t *testing.T) {
	store := testStore(t)
	journal, err := store.Begin(Update, lifecyclePlan("v1.2.4"), "v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	journal = transition(t, store, journal, BackupComplete, Staged, Applied, Verified, Committed)
	if err := journal.Validate(); err != nil {
		t.Fatalf("committed update journal invalid: %v", err)
	}
}

func TestSecondOperationIsRejectedWhileActive(t *testing.T) {
	store := testStore(t)
	journal, err := store.Begin(Install, lifecyclePlan("v1.2.3"), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Begin(Install, lifecyclePlan("v1.2.3"), ""); err == nil {
		t.Fatal("second active operation was accepted")
	}
	transition(t, store, journal, Failed)
	if _, err := store.Begin(Install, lifecyclePlan("v1.2.3"), ""); err != nil {
		t.Fatalf("new operation rejected after terminal failure: %v", err)
	}
}

func TestJournalHeaderTamperingIsDetected(t *testing.T) {
	store := testStore(t)
	journal, err := store.Begin(Install, lifecyclePlan("v1.2.3"), "")
	if err != nil {
		t.Fatal(err)
	}
	journalPath := filepath.Join(store.deploymentDirectory(journal.DeploymentID), "operations", journal.OperationID+".json")
	contents, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(contents, &raw); err != nil {
		t.Fatal(err)
	}
	raw["toVersion"] = "v9.9.9"
	contents, _ = json.Marshal(raw)
	if err := os.WriteFile(journalPath, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Active(journal.DeploymentID); err == nil {
		t.Fatal("tampered journal was accepted")
	}
}

func TestDeploymentIDIgnoresReleaseAndDisplayName(t *testing.T) {
	first := lifecyclePlan("v1.2.3")
	second := lifecyclePlan("v1.2.4")
	second.Name = "Renamed deployment"
	firstID, err := DeploymentID(first)
	if err != nil {
		t.Fatal(err)
	}
	secondID, err := DeploymentID(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstID != secondID {
		t.Fatalf("stable target identity changed across update: %s != %s", firstID, secondID)
	}
}
