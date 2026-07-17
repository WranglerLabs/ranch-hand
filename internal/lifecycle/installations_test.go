package lifecycle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/WranglerLabs/ranch-hand/internal/plan"
)

func installationPlan(version string) plan.DeploymentPlan {
	return plan.DeploymentPlan{
		SchemaVersion: plan.CurrentSchemaVersion,
		Name:          "Recorded local install",
		Release: plan.ReleaseSelection{
			Version: version, ManifestURL: "https://github.com/WranglerLabs/repo-wrangler/releases/download/" + version + "/release-manifest.json",
			ManifestSHA256: strings.Repeat("a", 64), ArtifactSHA256: strings.Repeat("b", 64), ArtifactSize: 42,
		},
		Target:        plan.Target{Kind: "local-compose"},
		Configuration: map[string]string{"projectName": "recorded", "dataVolume": "recorded-data", "listenAddress": "127.0.0.1:8080"},
	}
}

func commitInstall(t *testing.T, store *Store, candidate plan.DeploymentPlan) Journal {
	t.Helper()
	journal, err := store.Begin(Install, candidate, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []Phase{Staged, Applied, Verified, Committed} {
		journal, err = store.Transition(journal.DeploymentID, journal.OperationID, phase)
		if err != nil {
			t.Fatalf("transition install to %s: %v", phase, err)
		}
	}
	return journal
}

func TestCommittedInstallAndUpdateAdvanceCurrentRecord(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	installed := commitInstall(t, store, installationPlan("v1.2.3"))
	record, err := store.Installation(installed.DeploymentID)
	if err != nil || record.Version != "v1.2.3" || record.State != InstallationActive || record.LastOperationID != installed.OperationID {
		t.Fatalf("installed state was not recorded: %+v, %v", record, err)
	}

	updatedPlan := installationPlan("v1.2.4")
	journal, err := store.Begin(Update, updatedPlan, "v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	journal, err = store.TransitionWithReference(journal.DeploymentID, journal.OperationID, BackupComplete, strings.Repeat("c", 32))
	if err != nil {
		t.Fatal(err)
	}
	for _, phase := range []Phase{Staged, Applied, Verified, Committed} {
		journal, err = store.Transition(journal.DeploymentID, journal.OperationID, phase)
		if err != nil {
			t.Fatalf("transition update to %s: %v", phase, err)
		}
	}
	record, err = store.Installation(journal.DeploymentID)
	if err != nil || record.Version != "v1.2.4" || record.LastOperationKind != Update ||
		record.LastEventHash != journal.Events[len(journal.Events)-1].Hash || !record.InstalledAt.Equal(installed.StartedAt) {
		t.Fatalf("updated state was not recorded: %+v, %v", record, err)
	}
}

func TestUpdateRejectsAStaleRecordedVersion(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	commitInstall(t, store, installationPlan("v1.2.3"))
	if _, err := store.Begin(Update, installationPlan("v1.2.5"), "v1.2.4"); err == nil || !strings.Contains(err.Error(), "recorded installed version") {
		t.Fatalf("stale update unexpectedly began: %v", err)
	}
}

func TestBeginReconcilesInterruptedCommittedRecordFinalization(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	journal := commitInstall(t, store, installationPlan("v1.2.3"))
	directory := store.deploymentDirectory(journal.DeploymentID)
	if err := os.Remove(filepath.Join(directory, "installation.json")); err != nil {
		t.Fatal(err)
	}
	if err := atomicWriteBytes(filepath.Join(directory, "active"), []byte(journal.OperationID+"\n")); err != nil {
		t.Fatal(err)
	}

	next, err := store.Begin(Backup, installationPlan("v1.2.3"), "v1.2.3")
	if err != nil {
		t.Fatalf("begin did not reconcile interrupted finalization: %v", err)
	}
	record, err := store.Installation(journal.DeploymentID)
	if err != nil || record.LastOperationID != journal.OperationID {
		t.Fatalf("committed installation was not rebuilt: %+v, %v", record, err)
	}
	if _, err := store.Transition(next.DeploymentID, next.OperationID, Failed); err != nil {
		t.Fatal(err)
	}
}

func TestInstallationRecordRejectsUnknownFields(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	journal := commitInstall(t, store, installationPlan("v1.2.3"))
	filename := filepath.Join(store.deploymentDirectory(journal.DeploymentID), "installation.json")
	contents, err := os.ReadFile(filename)
	if err != nil {
		t.Fatal(err)
	}
	tampered := strings.Replace(string(contents), "\n}", ",\n  \"secret\": \"must-not-be-accepted\"\n}", 1)
	if err := os.WriteFile(filename, []byte(tampered), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Installation(journal.DeploymentID); err == nil {
		t.Fatal("installation record with an unknown field was accepted")
	}
}

func TestListsRecordedInstallations(t *testing.T) {
	store, err := NewStore(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	first := installationPlan("v1.2.3")
	second := installationPlan("v1.2.4")
	second.Configuration["projectName"] = "second"
	second.Configuration["dataVolume"] = "second-data"
	commitInstall(t, store, first)
	commitInstall(t, store, second)
	records, err := store.Installations()
	if err != nil || len(records) != 2 {
		t.Fatalf("installation inventory failed: %+v, %v", records, err)
	}
	if records[0].UpdatedAt.Before(records[1].UpdatedAt) {
		t.Fatal("installation inventory is not newest-first")
	}
}
