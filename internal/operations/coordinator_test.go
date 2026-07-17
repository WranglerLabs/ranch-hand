package operations

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/WranglerLabs/ranch-hand/internal/adapter"
	"github.com/WranglerLabs/ranch-hand/internal/bundle"
	"github.com/WranglerLabs/ranch-hand/internal/lifecycle"
	"github.com/WranglerLabs/ranch-hand/internal/plan"
	productrelease "github.com/WranglerLabs/ranch-hand/internal/release"
)

func operationPlan(version string) plan.DeploymentPlan {
	return plan.DeploymentPlan{
		SchemaVersion: plan.CurrentSchemaVersion, Name: "Local RepoWrangler",
		Release: plan.ReleaseSelection{
			Version: version, ManifestURL: "https://github.com/WranglerLabs/repo-wrangler/releases/download/" + version + "/release-manifest.json",
			ManifestSHA256: strings.Repeat("a", 64), ArtifactSHA256: strings.Repeat("b", 64), ArtifactSize: 42,
		},
		Target:        plan.Target{Kind: "local-compose"},
		Configuration: map[string]string{"projectName": "repo-wrangler", "dataVolume": "repo-wrangler-data", "listenAddress": "127.0.0.1:8080"},
	}
}

func operationArtifact(candidate plan.DeploymentPlan) productrelease.VerifiedArtifact {
	return productrelease.VerifiedArtifact{
		Product: productrelease.Product, Version: candidate.Release.Version, Target: candidate.Target.Kind,
		ManifestURL: candidate.Release.ManifestURL, ManifestSHA256: candidate.Release.ManifestSHA256,
		SHA256: candidate.Release.ArtifactSHA256, Size: candidate.Release.ArtifactSize, CachePath: `C:\cache\bundle.tar.gz`,
		ProvenanceVerified: true, SBOMVerified: true,
	}
}

type fakeStager struct {
	calls int
	err   error
}

func (f *fakeStager) Stage(artifact productrelease.VerifiedArtifact) (bundle.StagedBundle, error) {
	f.calls++
	if f.err != nil {
		return bundle.StagedBundle{}, f.err
	}
	return bundle.StagedBundle{Product: artifact.Product, Version: artifact.Version, Target: artifact.Target, Path: `C:\stage\bundle`}, nil
}

type fakeMutator struct {
	calls           []string
	applyError      error
	verifyError     error
	recoverError    error
	appliedBackup   *lifecycle.BackupRecord
	recoveredBackup *lifecycle.BackupRecord
}

type failingTransitionStore struct {
	*lifecycle.Store
	phase  lifecycle.Phase
	failed bool
}

func (f *failingTransitionStore) Transition(deploymentID, operationID string, phase lifecycle.Phase) (lifecycle.Journal, error) {
	if phase == f.phase && !f.failed {
		f.failed = true
		return lifecycle.Journal{}, errors.New("simulated journal write failure")
	}
	return f.Store.Transition(deploymentID, operationID, phase)
}

func (f *fakeMutator) Backup(_ context.Context, _ plan.DeploymentPlan, _ adapter.Credentials) (lifecycle.BackupArtifact, error) {
	f.calls = append(f.calls, "backup")
	return lifecycle.BackupArtifact{Kind: lifecycle.LocalArchive, Locator: "backups/current.tar.gz", Size: 42, SHA256: strings.Repeat("c", 64)}, nil
}

func (f *fakeMutator) Apply(_ context.Context, _ lifecycle.OperationKind, _ plan.DeploymentPlan, _ bundle.StagedBundle, backup *lifecycle.BackupRecord, _ adapter.Credentials) error {
	f.calls = append(f.calls, "apply")
	if backup != nil {
		f.appliedBackup = backup
	}
	return f.applyError
}

func (f *fakeMutator) Verify(_ context.Context, _ plan.DeploymentPlan, _ adapter.Credentials) error {
	f.calls = append(f.calls, "verify")
	return f.verifyError
}

func (f *fakeMutator) Recover(_ context.Context, _ lifecycle.OperationKind, _ plan.DeploymentPlan, backup *lifecycle.BackupRecord, _ adapter.Credentials) error {
	f.calls = append(f.calls, "recover")
	f.recoveredBackup = backup
	return f.recoverError
}

func coordinatorForTest(t *testing.T, mutator *fakeMutator, staged *fakeStager) *Coordinator {
	t.Helper()
	store, err := lifecycle.NewStore(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := NewCoordinator(store, staged, NewRegistry(map[string]Mutator{"local-compose": mutator}))
	if err != nil {
		t.Fatal(err)
	}
	return coordinator
}

func seedInstalledVersion(t *testing.T, coordinator *Coordinator, mutator *fakeMutator, staged *fakeStager, version string) {
	t.Helper()
	candidate := operationPlan(version)
	if result, err := coordinator.Run(context.Background(), Request{Kind: lifecycle.Install, Plan: candidate, Artifact: operationArtifact(candidate)}); err != nil || result.Journal.Phase != lifecycle.Committed {
		t.Fatalf("seed install failed: %+v, %v", result, err)
	}
	mutator.calls = nil
	mutator.appliedBackup = nil
	mutator.recoveredBackup = nil
	staged.calls = 0
}

func TestInstallCommitsAfterApplyAndVerify(t *testing.T) {
	mutator, staged := &fakeMutator{}, &fakeStager{}
	coordinator := coordinatorForTest(t, mutator, staged)
	candidate := operationPlan("v1.2.3")
	result, err := coordinator.Run(context.Background(), Request{Kind: lifecycle.Install, Plan: candidate, Artifact: operationArtifact(candidate)})
	if err != nil || result.Journal.Phase != lifecycle.Committed {
		t.Fatalf("install failed: %+v, %v", result, err)
	}
	if strings.Join(mutator.calls, ",") != "apply,verify" || staged.calls != 1 {
		t.Fatalf("unexpected operation order: %v, stage=%d", mutator.calls, staged.calls)
	}
}

func TestUpdateBacksUpBeforeApply(t *testing.T) {
	mutator, staged := &fakeMutator{}, &fakeStager{}
	coordinator := coordinatorForTest(t, mutator, staged)
	seedInstalledVersion(t, coordinator, mutator, staged, "v1.2.3")
	candidate := operationPlan("v1.2.4")
	result, err := coordinator.Run(context.Background(), Request{Kind: lifecycle.Update, Plan: candidate, FromVersion: "v1.2.3", Artifact: operationArtifact(candidate)})
	if err != nil || result.Journal.Phase != lifecycle.Committed || result.Backup == nil {
		t.Fatalf("update failed: %+v, %v", result, err)
	}
	if strings.Join(mutator.calls, ",") != "backup,apply,verify" {
		t.Fatalf("backup-first ordering violated: %v", mutator.calls)
	}
	if mutator.appliedBackup == nil || mutator.appliedBackup.BackupID != result.Backup.BackupID {
		t.Fatal("apply did not receive the exact recorded update backup")
	}
}

func TestFailedVerificationRecoversExactBackup(t *testing.T) {
	mutator := &fakeMutator{}
	staged := &fakeStager{}
	coordinator := coordinatorForTest(t, mutator, staged)
	seedInstalledVersion(t, coordinator, mutator, staged, "v1.2.3")
	mutator.verifyError = errors.New("unhealthy")
	candidate := operationPlan("v1.2.4")
	result, err := coordinator.Run(context.Background(), Request{Kind: lifecycle.Update, Plan: candidate, FromVersion: "v1.2.3", Artifact: operationArtifact(candidate)})
	if err == nil || !result.Recovered || result.Journal.Phase != lifecycle.Recovered {
		t.Fatalf("failed update did not recover: %+v, %v", result, err)
	}
	if result.Backup == nil || mutator.recoveredBackup == nil || result.Backup.BackupID != mutator.recoveredBackup.BackupID {
		t.Fatal("recovery did not receive the recorded update backup")
	}
}

func TestBackupOperationDoesNotStageOrApply(t *testing.T) {
	mutator, staged := &fakeMutator{}, &fakeStager{}
	coordinator := coordinatorForTest(t, mutator, staged)
	seedInstalledVersion(t, coordinator, mutator, staged, "v1.2.3")
	candidate := operationPlan("v1.2.3")
	result, err := coordinator.Run(context.Background(), Request{Kind: lifecycle.Backup, Plan: candidate, FromVersion: "v1.2.3"})
	if err != nil || result.Journal.Phase != lifecycle.Committed || result.Backup == nil {
		t.Fatalf("backup operation failed: %+v, %v", result, err)
	}
	if strings.Join(mutator.calls, ",") != "backup" || staged.calls != 0 {
		t.Fatalf("backup performed release mutation: %v, stage=%d", mutator.calls, staged.calls)
	}
}

func TestAppliedJournalFailureTriggersRecovery(t *testing.T) {
	base, err := lifecycle.NewStore(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	store := &failingTransitionStore{Store: base, phase: lifecycle.Applied}
	mutator := &fakeMutator{}
	coordinator, err := NewCoordinator(store, &fakeStager{}, NewRegistry(map[string]Mutator{"local-compose": mutator}))
	if err != nil {
		t.Fatal(err)
	}
	candidate := operationPlan("v1.2.3")
	result, err := coordinator.Run(context.Background(), Request{Kind: lifecycle.Install, Plan: candidate, Artifact: operationArtifact(candidate)})
	if err == nil || !result.Recovered || !strings.Contains(strings.Join(mutator.calls, ","), "recover") {
		t.Fatalf("journal failure after apply did not recover target: %+v, %v, calls=%v", result, err, mutator.calls)
	}
}
