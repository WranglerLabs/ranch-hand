package lifecycle

import (
	"strings"
	"testing"
)

func TestRecordAndListBackup(t *testing.T) {
	store := testStore(t)
	commitInstall(t, store, lifecyclePlan("v1.2.3"))
	journal, err := store.Begin(Update, lifecyclePlan("v1.2.4"), "v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.RecordBackup(journal, BackupArtifact{
		Kind: LocalArchive, Locator: "backups/repo-wrangler-v1.2.3.tar.gz",
		Size: 42, SHA256: strings.Repeat("a", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	journal, err = store.TransitionWithReference(journal.DeploymentID, journal.OperationID, BackupComplete, record.BackupID)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Backup(journal.DeploymentID, record.BackupID)
	if err != nil || loaded.BackupID != record.BackupID {
		t.Fatalf("backup lookup failed: %+v, %v", loaded, err)
	}
	listed, err := store.Backups(journal.DeploymentID)
	if err != nil || len(listed) != 1 || listed[0].BackupID != record.BackupID {
		t.Fatalf("backup listing failed: %+v, %v", listed, err)
	}
}

func TestRejectsCredentialBearingBackupLocator(t *testing.T) {
	artifact := BackupArtifact{Kind: AzureSnapshot, Locator: "/subscriptions/id/snapshots/one?sig=secret"}
	if err := artifact.Validate(); err == nil {
		t.Fatal("credential-bearing backup locator was accepted")
	}
}

func TestBackupRequiresActivePreparedOperation(t *testing.T) {
	store := testStore(t)
	commitInstall(t, store, lifecyclePlan("v1.2.3"))
	journal, err := store.Begin(Update, lifecyclePlan("v1.2.4"), "v1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	journal = transition(t, store, journal, Staged)
	_, err = store.RecordBackup(journal, BackupArtifact{Kind: AzureSnapshot, Locator: "/subscriptions/id/snapshots/one"})
	if err == nil {
		t.Fatal("backup was recorded after prepared phase")
	}
}
