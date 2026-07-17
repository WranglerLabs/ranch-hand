package diagnostics

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"runtime"
	"time"

	"github.com/WranglerLabs/ranch-hand/internal/lifecycle"
)

const SchemaVersion = "1.0"

type Source interface {
	Installations() ([]lifecycle.InstallationRecord, error)
	Backups(string) ([]lifecycle.BackupRecord, error)
	Active(string) (lifecycle.Journal, error)
}

type Snapshot struct {
	SchemaVersion string       `json:"schemaVersion"`
	GeneratedAt   time.Time    `json:"generatedAt"`
	Application   Application  `json:"application"`
	Installations []Deployment `json:"installations"`
}

type Application struct {
	Name     string `json:"name"`
	Version  string `json:"version"`
	Platform string `json:"platform"`
	Arch     string `json:"arch"`
}

type Deployment struct {
	DeploymentID      string     `json:"deploymentId"`
	Target            string     `json:"target"`
	State             string     `json:"state"`
	Version           string     `json:"version"`
	InstalledAt       time.Time  `json:"installedAt"`
	UpdatedAt         time.Time  `json:"updatedAt"`
	LastOperationID   string     `json:"lastOperationId"`
	LastOperationKind string     `json:"lastOperationKind"`
	LastEventHash     string     `json:"lastEventHash"`
	ActiveOperation   *Operation `json:"activeOperation,omitempty"`
	Backups           []Backup   `json:"backups"`
}

type Operation struct {
	OperationID   string    `json:"operationId"`
	Kind          string    `json:"kind"`
	FromVersion   string    `json:"fromVersion,omitempty"`
	ToVersion     string    `json:"toVersion"`
	Phase         string    `json:"phase"`
	UpdatedAt     time.Time `json:"updatedAt"`
	InputBackupID string    `json:"inputBackupId,omitempty"`
}

type Backup struct {
	BackupID    string    `json:"backupId"`
	OperationID string    `json:"operationId"`
	Version     string    `json:"version"`
	CreatedAt   time.Time `json:"createdAt"`
	Kind        string    `json:"kind"`
	Size        int64     `json:"size,omitempty"`
	SHA256      string    `json:"sha256,omitempty"`
}

type Collector struct {
	Now    func() time.Time
	Random io.Reader
}

func (c Collector) Collect(applicationVersion string, source Source) (Snapshot, error) {
	now := c.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	snapshot := Snapshot{
		SchemaVersion: SchemaVersion, GeneratedAt: now().UTC(),
		Application:   Application{Name: "Ranch Hand", Version: applicationVersion, Platform: runtime.GOOS, Arch: runtime.GOARCH},
		Installations: []Deployment{},
	}
	random := c.Random
	if random == nil {
		random = rand.Reader
	}
	salt := make([]byte, 32)
	if _, err := io.ReadFull(random, salt); err != nil {
		return Snapshot{}, errors.New("create diagnostics pseudonym salt")
	}
	pseudonym := func(deploymentID string) string {
		digest := sha256.Sum256(append(append([]byte(nil), salt...), []byte(deploymentID)...))
		return "deployment-" + hex.EncodeToString(digest[:12])
	}
	records, err := source.Installations()
	if err != nil {
		return Snapshot{}, err
	}
	for _, record := range records {
		deployment := Deployment{
			DeploymentID: pseudonym(record.DeploymentID), Target: record.Target, State: string(record.State), Version: record.Version,
			InstalledAt: record.InstalledAt, UpdatedAt: record.UpdatedAt,
			LastOperationID: record.LastOperationID, LastOperationKind: string(record.LastOperationKind), LastEventHash: record.LastEventHash,
			Backups: []Backup{},
		}
		active, activeErr := source.Active(record.DeploymentID)
		if activeErr == nil {
			deployment.ActiveOperation = &Operation{
				OperationID: active.OperationID, Kind: string(active.Kind), FromVersion: active.FromVersion,
				ToVersion: active.ToVersion, Phase: string(active.Phase), UpdatedAt: active.UpdatedAt, InputBackupID: active.InputBackupID,
			}
		} else if !errors.Is(activeErr, os.ErrNotExist) {
			return Snapshot{}, activeErr
		}
		backups, err := source.Backups(record.DeploymentID)
		if err != nil {
			return Snapshot{}, err
		}
		for _, record := range backups {
			deployment.Backups = append(deployment.Backups, Backup{
				BackupID: record.BackupID, OperationID: record.OperationID, Version: record.Version,
				CreatedAt: record.CreatedAt, Kind: string(record.Artifact.Kind), Size: record.Artifact.Size, SHA256: record.Artifact.SHA256,
			})
		}
		snapshot.Installations = append(snapshot.Installations, deployment)
	}
	return snapshot, nil
}
