package operations

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/WranglerLabs/ranch-hand/internal/adapter"
	"github.com/WranglerLabs/ranch-hand/internal/bundle"
	"github.com/WranglerLabs/ranch-hand/internal/lifecycle"
	"github.com/WranglerLabs/ranch-hand/internal/plan"
	productrelease "github.com/WranglerLabs/ranch-hand/internal/release"
)

type journalStore interface {
	Begin(lifecycle.OperationKind, plan.DeploymentPlan, string) (lifecycle.Journal, error)
	Transition(string, string, lifecycle.Phase) (lifecycle.Journal, error)
	TransitionWithReference(string, string, lifecycle.Phase, string) (lifecycle.Journal, error)
	RecordBackup(lifecycle.Journal, lifecycle.BackupArtifact) (lifecycle.BackupRecord, error)
}

type stager interface {
	Stage(productrelease.VerifiedArtifact) (bundle.StagedBundle, error)
}

type Mutator interface {
	Backup(context.Context, plan.DeploymentPlan, adapter.Credentials) (lifecycle.BackupArtifact, error)
	Apply(context.Context, lifecycle.OperationKind, plan.DeploymentPlan, bundle.StagedBundle, *lifecycle.BackupRecord, adapter.Credentials) error
	Verify(context.Context, plan.DeploymentPlan, adapter.Credentials) error
	Recover(context.Context, lifecycle.OperationKind, plan.DeploymentPlan, *lifecycle.BackupRecord, adapter.Credentials) error
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
	Artifact    productrelease.VerifiedArtifact
	Credentials adapter.Credentials
}

type Result struct {
	Journal   lifecycle.Journal       `json:"journal"`
	Backup    *lifecycle.BackupRecord `json:"backup,omitempty"`
	Recovered bool                    `json:"recovered"`
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
	if request.Kind != lifecycle.Install && request.Kind != lifecycle.Update && request.Kind != lifecycle.Backup {
		return Result{}, fmt.Errorf("%s coordinator is not implemented yet", request.Kind)
	}
	if request.Kind != lifecycle.Backup {
		if err := artifactMatchesPlan(request.Artifact, request.Plan); err != nil {
			return Result{}, err
		}
	}
	mutator, ok := c.targets.Target(request.Plan.Target.Kind)
	if !ok {
		return Result{}, fmt.Errorf("no lifecycle mutator is registered for target %q", request.Plan.Target.Kind)
	}
	journal, err := c.store.Begin(request.Kind, request.Plan, request.FromVersion)
	if err != nil {
		return Result{}, err
	}
	result := Result{Journal: journal}

	if request.Kind == lifecycle.Update || request.Kind == lifecycle.Backup {
		artifact, backupErr := mutator.Backup(ctx, request.Plan, request.Credentials)
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
	if err := mutator.Apply(ctx, request.Kind, request.Plan, staged, result.Backup, request.Credentials); err != nil {
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
	if err := mutator.Recover(ctx, request.Kind, request.Plan, result.Backup, request.Credentials); err != nil {
		if failed, failErr := c.store.Transition(result.Journal.DeploymentID, result.Journal.OperationID, lifecycle.Failed); failErr == nil {
			result.Journal = failed
		}
		return result, errors.Join(operationErr, fmt.Errorf("recover target: %w", err))
	}
	recovered, transitionErr := c.store.Transition(result.Journal.DeploymentID, result.Journal.OperationID, lifecycle.Recovered)
	if transitionErr == nil {
		result.Journal = recovered
		result.Recovered = true
	}
	return result, errors.Join(operationErr, transitionErr)
}
