package operations

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/WranglerLabs/ranch-hand/internal/adapter"
	"github.com/WranglerLabs/ranch-hand/internal/bundle"
	"github.com/WranglerLabs/ranch-hand/internal/lifecycle"
	"github.com/WranglerLabs/ranch-hand/internal/plan"
	productrelease "github.com/WranglerLabs/ranch-hand/internal/release"
)

type journalStore interface {
	BeginWithInputBackup(lifecycle.OperationKind, plan.DeploymentPlan, string, string) (lifecycle.Journal, error)
	Transition(string, string, lifecycle.Phase) (lifecycle.Journal, error)
	TransitionWithReference(string, string, lifecycle.Phase, string) (lifecycle.Journal, error)
	RecordBackup(lifecycle.Journal, lifecycle.BackupArtifact) (lifecycle.BackupRecord, error)
	Backup(string, string) (lifecycle.BackupRecord, error)
	Active(string) (lifecycle.Journal, error)
}

type stager interface {
	Stage(productrelease.VerifiedArtifact) (bundle.StagedBundle, error)
}

type Mutator interface {
	Backup(context.Context, plan.DeploymentPlan, string, adapter.Credentials) (lifecycle.BackupArtifact, error)
	Apply(context.Context, lifecycle.OperationKind, plan.DeploymentPlan, string, bundle.StagedBundle, lifecycle.OperationBackups, adapter.Credentials) error
	Verify(context.Context, plan.DeploymentPlan, adapter.Credentials) error
	Recover(context.Context, lifecycle.OperationKind, plan.DeploymentPlan, string, lifecycle.OperationBackups, adapter.Credentials) error
}

type Registry struct {
	targets map[string]Mutator
}

func NewRegistry(targets map[string]Mutator) *Registry {
	copyOfTargets := make(map[string]Mutator, len(targets))
	for target, mutator := range targets {
		copyOfTargets[target] = mutator
	}
	return &Registry{targets: copyOfTargets}
}

func (r *Registry) Target(target string) (Mutator, bool) {
	mutator, ok := r.targets[target]
	return mutator, ok
}

type Request struct {
	Kind        lifecycle.OperationKind
	Plan        plan.DeploymentPlan
	FromVersion string
	BackupID    string
	Artifact    productrelease.VerifiedArtifact
	Credentials adapter.Credentials
}

type Result struct {
	Journal        lifecycle.Journal       `json:"journal"`
	Backup         *lifecycle.BackupRecord `json:"backup,omitempty"`
	SelectedBackup *lifecycle.BackupRecord `json:"selectedBackup,omitempty"`
	Recovered      bool                    `json:"recovered"`
	SafelyClosed   bool                    `json:"safelyClosed"`
}

func (c *Coordinator) RecoverActive(ctx context.Context, deploymentID string, credentials adapter.Credentials) (Result, error) {
	defer credentials.Clear()
	if ctx == nil {
		return Result{}, errors.New("recovery context is required")
	}
	if err := credentials.Validate(); err != nil {
		return Result{}, err
	}
	journal, err := c.store.Active(deploymentID)
	if err != nil {
		return Result{}, err
	}
	result := Result{Journal: journal}
	if journal.Phase == lifecycle.Prepared || journal.Phase == lifecycle.BackupComplete {
		closed, err := c.store.Transition(journal.DeploymentID, journal.OperationID, lifecycle.Failed)
		if err == nil {
			result.Journal = closed
			result.SafelyClosed = true
		}
		return result, err
	}
	candidate, err := plan.DecodeAndValidate(journal.Plan)
	if err != nil {
		return result, errors.New("active operation plan snapshot is invalid")
	}
	mutator, ok := c.targets.Target(journal.Target)
	if !ok {
		return result, fmt.Errorf("no lifecycle mutator is registered for target %q", journal.Target)
	}
	backups := lifecycle.OperationBackups{}
	if journal.InputBackupID != "" {
		selected, err := c.store.Backup(journal.DeploymentID, journal.InputBackupID)
		if err != nil {
			return result, fmt.Errorf("load active operation input backup: %w", err)
		}
		backups.Selected = &selected
	}
	for _, event := range journal.Events {
		if event.Phase == lifecycle.BackupComplete {
			safety, err := c.store.Backup(journal.DeploymentID, event.ReferenceID)
			if err != nil {
				return result, fmt.Errorf("load active operation safety backup: %w", err)
			}
			backups.Safety = &safety
		}
	}
	if journal.Phase != lifecycle.RecoveryStarted {
		if journal.Phase != lifecycle.Staged && journal.Phase != lifecycle.Applied && journal.Phase != lifecycle.Verified {
			return result, fmt.Errorf("active operation in phase %s cannot enter recovery", journal.Phase)
		}
		referenceID := ""
		if backups.Safety != nil {
			referenceID = backups.Safety.BackupID
		}
		journal, err = c.store.TransitionWithReference(journal.DeploymentID, journal.OperationID, lifecycle.RecoveryStarted, referenceID)
		if err != nil {
			return result, err
		}
		result.Journal = journal
	}
	recoveryContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 35*time.Minute)
	defer cancel()
	if err := mutator.Recover(recoveryContext, journal.Kind, candidate, journal.FromVersion, backups, credentials); err != nil {
		return result, fmt.Errorf("recover active target: %w; operation remains locked in recovery-started for retry", err)
	}
	recovered, err := c.store.Transition(journal.DeploymentID, journal.OperationID, lifecycle.Recovered)
	if err == nil {
		result.Journal = recovered
		result.Recovered = true
	}
	return result, err
}

type Coordinator struct {
	store   journalStore
	stager  stager
	targets *Registry
}

func NewCoordinator(store journalStore, stager stager, targets *Registry) (*Coordinator, error) {
	if store == nil || stager == nil || targets == nil {
		return nil, errors.New("lifecycle store, bundle stager, and target registry are required")
	}
	return &Coordinator{store: store, stager: stager, targets: targets}, nil
}

func (c *Coordinator) Run(ctx context.Context, request Request) (Result, error) {
	defer request.Credentials.Clear()
	if ctx == nil {
		return Result{}, errors.New("operation context is required")
	}
	if err := request.Credentials.Validate(); err != nil {
		return Result{}, err
	}
	if err := request.Plan.Validate(); err != nil {
		return Result{}, err
	}
	if request.Kind != lifecycle.Install && request.Kind != lifecycle.Update && request.Kind != lifecycle.Backup && request.Kind != lifecycle.Restore && request.Kind != lifecycle.Rollback && request.Kind != lifecycle.Repair && request.Kind != lifecycle.Uninstall {
		return Result{}, fmt.Errorf("%s coordinator is not implemented yet", request.Kind)
	}
	if request.Kind != lifecycle.Backup && request.Kind != lifecycle.Uninstall {
		if err := artifactMatchesPlan(request.Artifact, request.Plan); err != nil {
			return Result{}, err
		}
	}
	mutator, ok := c.targets.Target(request.Plan.Target.Kind)
	if !ok {
		return Result{}, fmt.Errorf("no lifecycle mutator is registered for target %q", request.Plan.Target.Kind)
	}
	result := Result{}
	if request.Kind == lifecycle.Restore || request.Kind == lifecycle.Rollback {
		deploymentID, identityErr := lifecycle.DeploymentID(request.Plan)
		if identityErr != nil {
			return Result{}, identityErr
		}
		selected, lookupErr := c.store.Backup(deploymentID, request.BackupID)
		if lookupErr != nil {
			return Result{}, fmt.Errorf("load selected backup: %w", lookupErr)
		}
		if selected.DeploymentID != deploymentID || selected.Target != request.Plan.Target.Kind || selected.Version != request.Plan.Release.Version {
			return Result{}, errors.New("selected backup does not match the deployment, target, and requested release")
		}
		if request.Kind == lifecycle.Restore && selected.Version != request.FromVersion {
			return Result{}, errors.New("restore requires a backup from the currently installed version")
		}
		if request.Kind == lifecycle.Rollback && selected.Version == request.FromVersion {
			return Result{}, errors.New("rollback requires a backup from a different prior version")
		}
		result.SelectedBackup = &selected
	}
	journal, err := c.store.BeginWithInputBackup(request.Kind, request.Plan, request.FromVersion, request.BackupID)
	if err != nil {
		return Result{}, err
	}
	result.Journal = journal
	if request.Kind == lifecycle.Uninstall {
		updated, transitionErr := c.store.Transition(journal.DeploymentID, journal.OperationID, lifecycle.Staged)
		if transitionErr != nil {
			return result, transitionErr
		}
		result.Journal = updated
		if applyErr := mutator.Apply(ctx, request.Kind, request.Plan, request.FromVersion, bundle.StagedBundle{}, lifecycle.OperationBackups{}, request.Credentials); applyErr != nil {
			return c.recover(ctx, request, mutator, result, fmt.Errorf("remove owned target deployment: %w", applyErr))
		}
		updated, transitionErr = c.store.Transition(journal.DeploymentID, journal.OperationID, lifecycle.Applied)
		if transitionErr != nil {
			return c.recover(ctx, request, mutator, result, fmt.Errorf("record removed target phase: %w", transitionErr))
		}
		result.Journal = updated
		updated, transitionErr = c.store.Transition(journal.DeploymentID, journal.OperationID, lifecycle.Verified)
		if transitionErr != nil {
			return c.recover(ctx, request, mutator, result, fmt.Errorf("record verified removal phase: %w", transitionErr))
		}
		result.Journal = updated
		updated, transitionErr = c.store.Transition(journal.DeploymentID, journal.OperationID, lifecycle.Committed)
		if transitionErr == nil {
			result.Journal = updated
		}
		return result, transitionErr
	}

	if request.Kind == lifecycle.Update || request.Kind == lifecycle.Backup || request.Kind == lifecycle.Restore || request.Kind == lifecycle.Rollback || request.Kind == lifecycle.Repair {
		artifact, backupErr := mutator.Backup(ctx, request.Plan, request.FromVersion, request.Credentials)
		if backupErr != nil {
			if failed, transitionErr := c.store.Transition(journal.DeploymentID, journal.OperationID, lifecycle.Failed); transitionErr == nil {
				result.Journal = failed
			}
			return result, fmt.Errorf("create target backup: %w", backupErr)
		}
		backup, recordErr := c.store.RecordBackup(result.Journal, artifact)
		if recordErr != nil {
			if failed, transitionErr := c.store.Transition(journal.DeploymentID, journal.OperationID, lifecycle.Failed); transitionErr == nil {
				result.Journal = failed
			}
			return result, fmt.Errorf("record target backup: %w", recordErr)
		}
		result.Backup = &backup
		updated, transitionErr := c.store.TransitionWithReference(journal.DeploymentID, journal.OperationID, lifecycle.BackupComplete, backup.BackupID)
		err = transitionErr
		if err != nil {
			return result, err
		}
		result.Journal = updated
		if request.Kind == lifecycle.Backup {
			updated, err = c.store.Transition(journal.DeploymentID, journal.OperationID, lifecycle.Committed)
			if err == nil {
				result.Journal = updated
			}
			return result, err
		}
	}

	staged, err := c.stager.Stage(request.Artifact)
	if err != nil {
		if failed, transitionErr := c.store.Transition(journal.DeploymentID, journal.OperationID, lifecycle.Failed); transitionErr == nil {
			result.Journal = failed
		}
		return result, fmt.Errorf("stage verified release: %w", err)
	}
	updated, err := c.store.Transition(journal.DeploymentID, journal.OperationID, lifecycle.Staged)
	if err != nil {
		return result, err
	}
	result.Journal = updated
	backups := lifecycle.OperationBackups{Selected: result.SelectedBackup, Safety: result.Backup}
	if err := mutator.Apply(ctx, request.Kind, request.Plan, request.FromVersion, staged, backups, request.Credentials); err != nil {
		return c.recover(ctx, request, mutator, result, fmt.Errorf("apply target release: %w", err))
	}
	updated, err = c.store.Transition(journal.DeploymentID, journal.OperationID, lifecycle.Applied)
	if err != nil {
		return c.recover(ctx, request, mutator, result, fmt.Errorf("record applied phase: %w", err))
	}
	result.Journal = updated
	if err := mutator.Verify(ctx, request.Plan, request.Credentials); err != nil {
		return c.recover(ctx, request, mutator, result, fmt.Errorf("verify target release: %w", err))
	}
	updated, err = c.store.Transition(journal.DeploymentID, journal.OperationID, lifecycle.Verified)
	if err != nil {
		return c.recover(ctx, request, mutator, result, fmt.Errorf("record verified phase: %w", err))
	}
	result.Journal = updated
	updated, err = c.store.Transition(journal.DeploymentID, journal.OperationID, lifecycle.Committed)
	if err == nil {
		result.Journal = updated
	}
	return result, err
}

func artifactMatchesPlan(artifact productrelease.VerifiedArtifact, candidate plan.DeploymentPlan) error {
	if artifact.Product != productrelease.Product || artifact.Version != candidate.Release.Version || artifact.Target != candidate.Target.Kind ||
		!strings.EqualFold(artifact.SHA256, candidate.Release.ArtifactSHA256) || artifact.Size != candidate.Release.ArtifactSize ||
		artifact.ManifestURL != candidate.Release.ManifestURL || !strings.EqualFold(artifact.ManifestSHA256, candidate.Release.ManifestSHA256) ||
		!artifact.ProvenanceVerified || !artifact.SBOMVerified || artifact.CachePath == "" {
		return errors.New("operation artifact does not match the plan's verified immutable release")
	}
	return nil
}

func (c *Coordinator) recover(ctx context.Context, request Request, mutator Mutator, result Result, operationErr error) (Result, error) {
	recoveryContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 35*time.Minute)
	defer cancel()
	referenceID := ""
	if result.Backup != nil {
		referenceID = result.Backup.BackupID
	}
	journal, transitionErr := c.store.TransitionWithReference(result.Journal.DeploymentID, result.Journal.OperationID, lifecycle.RecoveryStarted, referenceID)
	if transitionErr != nil {
		if failed, failErr := c.store.Transition(result.Journal.DeploymentID, result.Journal.OperationID, lifecycle.Failed); failErr == nil {
			result.Journal = failed
		}
		return result, errors.Join(operationErr, transitionErr)
	}
	result.Journal = journal
	backups := lifecycle.OperationBackups{Selected: result.SelectedBackup, Safety: result.Backup}
	if err := mutator.Recover(recoveryContext, request.Kind, request.Plan, request.FromVersion, backups, request.Credentials); err != nil {
		return result, errors.Join(operationErr, fmt.Errorf("recover target: %w; operation remains locked in recovery-started for retry", err))
	}
	recovered, transitionErr := c.store.Transition(result.Journal.DeploymentID, result.Journal.OperationID, lifecycle.Recovered)
	if transitionErr == nil {
		result.Journal = recovered
		result.Recovered = true
	}
	return result, errors.Join(operationErr, transitionErr)
}
