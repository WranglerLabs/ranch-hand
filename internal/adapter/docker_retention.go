package adapter

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"time"

	"github.com/WranglerLabs/ranch-hand/internal/lifecycle"
	"github.com/WranglerLabs/ranch-hand/internal/plan"
)

const maximumRollbackPoolRecords = 1000

type RollbackPoolEntry struct {
	BackupID      string    `json:"backupId"`
	Version       string    `json:"version"`
	CreatedAt     time.Time `json:"createdAt"`
	ContainerName string    `json:"containerName"`
	VolumeName    string    `json:"volumeName"`
}

type RollbackPruneResult struct {
	Kept   []RollbackPoolEntry `json:"kept"`
	Pruned []RollbackPoolEntry `json:"pruned"`
}

func (d *LocalDocker) RollbackPool(ctx context.Context, candidate plan.DeploymentPlan, backups []lifecycle.BackupRecord) ([]RollbackPoolEntry, error) {
	project, _, _, _, err := localDockerInputs(candidate)
	if err != nil {
		return nil, err
	}
	if len(backups) > maximumRollbackPoolRecords {
		return nil, errors.New("rollback pool inventory exceeds the 1000-record safety limit")
	}
	deploymentID, err := lifecycle.DeploymentID(candidate)
	if err != nil {
		return nil, err
	}
	entries := make([]RollbackPoolEntry, 0)
	for _, backup := range backups {
		if err := backup.Validate(); err != nil || backup.DeploymentID != deploymentID || backup.Target != "local-compose" {
			return nil, errors.New("rollback pool backup inventory does not match the local deployment")
		}
		containerName := rollbackContainerName(project, backup.BackupID)
		exists, metadata, err := d.containerMetadata(ctx, containerName)
		if err != nil {
			return nil, fmt.Errorf("inspect rollback container %s: %w", backup.BackupID, err)
		}
		if !exists {
			continue
		}
		if metadata.Running {
			return nil, fmt.Errorf("rollback container %s is unexpectedly running", backup.BackupID)
		}
		if err := verifyOwnership(metadata.Labels, deploymentID, "rollback container"); err != nil {
			return nil, err
		}
		if metadata.Labels["com.wranglerlabs.ranch-hand.version"] != backup.Version || metadata.DataVolume == "" {
			return nil, errors.New("rollback container release or data-volume identity does not match its backup record")
		}
		if err := d.verifyManagedVolume(ctx, metadata.DataVolume, deploymentID); err != nil {
			return nil, fmt.Errorf("verify rollback volume %s: %w", backup.BackupID, err)
		}
		entries = append(entries, RollbackPoolEntry{
			BackupID: backup.BackupID, Version: backup.Version, CreatedAt: backup.CreatedAt,
			ContainerName: containerName, VolumeName: metadata.DataVolume,
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].CreatedAt.Equal(entries[j].CreatedAt) {
			return entries[i].BackupID > entries[j].BackupID
		}
		return entries[i].CreatedAt.After(entries[j].CreatedAt)
	})
	return entries, nil
}

func (d *LocalDocker) PruneRollbackPool(ctx context.Context, candidate plan.DeploymentPlan, backups []lifecycle.BackupRecord, keepLatest int) (RollbackPruneResult, error) {
	if keepLatest < 0 || keepLatest > 10 {
		return RollbackPruneResult{}, errors.New("rollback retention must keep between 0 and 10 entries")
	}
	entries, err := d.RollbackPool(ctx, candidate, backups)
	if err != nil {
		return RollbackPruneResult{}, err
	}
	result := RollbackPruneResult{Kept: append([]RollbackPoolEntry(nil), entries[:min(keepLatest, len(entries))]...), Pruned: []RollbackPoolEntry{}}
	for _, entry := range entries[min(keepLatest, len(entries)):] {
		exists, metadata, err := d.containerMetadata(ctx, entry.ContainerName)
		if err != nil || !exists || metadata.Running || metadata.DataVolume != entry.VolumeName {
			return result, errors.New("rollback pool changed after verification; pruning stopped")
		}
		deploymentID, identityErr := lifecycle.DeploymentID(candidate)
		if identityErr != nil || verifyOwnership(metadata.Labels, deploymentID, "rollback container") != nil || metadata.Labels["com.wranglerlabs.ranch-hand.version"] != entry.Version {
			return result, errors.New("rollback pool ownership changed after verification; pruning stopped")
		}
		if err := d.verifyManagedVolume(ctx, entry.VolumeName, deploymentID); err != nil {
			return result, errors.New("rollback volume ownership changed after verification; pruning stopped")
		}
		if err := d.dockerJSON(ctx, http.MethodDelete, "/containers/"+url.PathEscape(metadata.ID), url.Values{"force": []string{"0"}, "v": []string{"0"}}, nil, http.StatusNoContent, nil); err != nil {
			return result, fmt.Errorf("remove verified rollback container: %w", err)
		}
		if err := d.removeManagedVolumeIfPresent(ctx, entry.VolumeName, deploymentID); err != nil {
			return result, fmt.Errorf("remove verified rollback volume %q after its container was removed: %w", entry.VolumeName, err)
		}
		result.Pruned = append(result.Pruned, entry)
	}
	return result, nil
}
