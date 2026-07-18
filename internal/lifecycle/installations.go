package lifecycle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/WranglerLabs/ranch-hand/internal/plan"
)

const InstallationSchemaVersion = "1.0"

type InstallationState string

const (
	InstallationActive      InstallationState = "active"
	InstallationUninstalled InstallationState = "uninstalled"
)

type InstallationRecord struct {
	SchemaVersion     string            `json:"schemaVersion"`
	DeploymentID      string            `json:"deploymentId"`
	Target            string            `json:"target"`
	State             InstallationState `json:"state"`
	Version           string            `json:"version"`
	PlanSHA256        string            `json:"planSha256"`
	Plan              json.RawMessage   `json:"plan"`
	InstalledAt       time.Time         `json:"installedAt"`
	UpdatedAt         time.Time         `json:"updatedAt"`
	LastOperationID   string            `json:"lastOperationId"`
	LastOperationKind OperationKind     `json:"lastOperationKind"`
	LastEventHash     string            `json:"lastEventHash"`
}

func (s *Store) Installation(deploymentID string) (InstallationRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !idPattern.MatchString(deploymentID) {
		return InstallationRecord{}, errors.New("invalid deployment identifier")
	}
	directory := s.deploymentDirectory(deploymentID)
	if err := rejectSymlinkComponents(s.root, directory); err != nil {
		return InstallationRecord{}, err
	}
	return readInstallation(filepath.Join(directory, "installation.json"))
}

func (s *Store) Installations() ([]InstallationRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	root := filepath.Join(s.root, "deployments")
	if err := rejectSymlinkComponents(s.root, root); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return []InstallationRecord{}, nil
	}
	if err != nil {
		return nil, err
	}
	records := make([]InstallationRecord, 0, len(entries))
	for _, entry := range entries {
		if !idPattern.MatchString(entry.Name()) || !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		directory := filepath.Join(root, entry.Name())
		if err := rejectSymlinkComponents(s.root, directory); err != nil {
			return nil, err
		}
		record, err := readInstallation(filepath.Join(directory, "installation.json"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if record.DeploymentID != entry.Name() {
			return nil, errors.New("installation record directory does not match its identity")
		}
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].UpdatedAt.After(records[j].UpdatedAt) })
	return records, nil
}

func (s *Store) validateOperationCurrentStateLocked(kind OperationKind, deploymentID, fromVersion string) error {
	record, err := readInstallation(filepath.Join(s.deploymentDirectory(deploymentID), "installation.json"))
	if kind == Install {
		if errors.Is(err, os.ErrNotExist) || (err == nil && record.State == InstallationUninstalled) {
			return nil
		}
		if err != nil {
			return err
		}
		return errors.New("deployment already has an active installation record")
	}
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errors.New("operation requires a Ranch Hand installation record")
		}
		return err
	}
	if record.State != InstallationActive {
		return errors.New("operation requires an active installation record")
	}
	if record.Version != fromVersion {
		return errors.New("operation fromVersion does not match the recorded installed version")
	}
	return nil
}

func (s *Store) recordInstallationLocked(journal Journal) (InstallationRecord, error) {
	if journal.Phase != Committed && !(journal.Kind == Uninstall && journal.Phase == Recovered) {
		return InstallationRecord{}, errors.New("installation state can only follow a committed operation or a recovered uninstall")
	}
	if err := journal.Validate(); err != nil {
		return InstallationRecord{}, err
	}
	if journal.Kind == Backup {
		return InstallationRecord{}, errors.New("backup does not change installation state")
	}
	directory := s.deploymentDirectory(journal.DeploymentID)
	installationPath := filepath.Join(directory, "installation.json")
	existing, existingErr := readInstallation(installationPath)
	if existingErr != nil && !errors.Is(existingErr, os.ErrNotExist) {
		return InstallationRecord{}, existingErr
	}
	if existingErr == nil && existing.LastOperationID == journal.OperationID {
		if existing.LastEventHash != journal.Events[len(journal.Events)-1].Hash {
			return InstallationRecord{}, errors.New("installation record conflicts with its committed operation")
		}
		return existing, nil
	}

	switch journal.Kind {
	case Install:
		if existingErr == nil && existing.State == InstallationActive {
			return InstallationRecord{}, errors.New("an active installation record already exists")
		}
	case Update, Restore, Repair, Rollback, Uninstall:
		if existingErr != nil || existing.State != InstallationActive {
			return InstallationRecord{}, errors.New("operation requires an active installation record")
		}
		if existing.Version != journal.FromVersion {
			return InstallationRecord{}, errors.New("operation fromVersion does not match the recorded installed version")
		}
	default:
		return InstallationRecord{}, errors.New("operation does not change installation state")
	}

	installedAt := journal.StartedAt
	if existingErr == nil && journal.Kind != Install {
		installedAt = existing.InstalledAt
	}
	state := InstallationActive
	version := journal.ToVersion
	planSnapshot := append(json.RawMessage(nil), journal.Plan...)
	planSHA256 := journal.PlanSHA256
	if journal.Kind == Uninstall {
		state = InstallationUninstalled
		version = existing.Version
		planSnapshot = append(json.RawMessage(nil), existing.Plan...)
		planSHA256 = existing.PlanSHA256
	}
	record := InstallationRecord{
		SchemaVersion: InstallationSchemaVersion, DeploymentID: journal.DeploymentID,
		Target: journal.Target, State: state, Version: version, PlanSHA256: planSHA256,
		Plan: planSnapshot, InstalledAt: installedAt,
		UpdatedAt: journal.UpdatedAt, LastOperationID: journal.OperationID,
		LastOperationKind: journal.Kind, LastEventHash: journal.Events[len(journal.Events)-1].Hash,
	}
	if err := record.Validate(); err != nil {
		return InstallationRecord{}, err
	}
	if err := atomicWrite(installationPath, record); err != nil {
		return InstallationRecord{}, err
	}
	return record, nil
}

func (r InstallationRecord) Validate() error {
	if r.SchemaVersion != InstallationSchemaVersion || !idPattern.MatchString(r.DeploymentID) ||
		!validLifecycleTargets[r.Target] || (r.State != InstallationActive && r.State != InstallationUninstalled) ||
		!versionPattern.MatchString(r.Version) || !digestPattern.MatchString(r.PlanSHA256) ||
		!idPattern.MatchString(r.LastOperationID) || !validKinds[r.LastOperationKind] ||
		!digestPattern.MatchString(r.LastEventHash) || r.InstalledAt.IsZero() || r.UpdatedAt.Before(r.InstalledAt) {
		return errors.New("installation record identity is invalid")
	}
	candidate, err := plan.DecodeAndValidate(r.Plan)
	if err != nil {
		return errors.New("installation record plan snapshot is invalid")
	}
	canonical, err := plan.CanonicalJSON(candidate)
	if err != nil {
		return errors.New("installation record plan snapshot is invalid")
	}
	digest := sha256.Sum256(canonical)
	deploymentID, err := DeploymentID(candidate)
	if err != nil || deploymentID != r.DeploymentID || candidate.Target.Kind != r.Target ||
		candidate.Release.Version != r.Version || hex.EncodeToString(digest[:]) != r.PlanSHA256 {
		return errors.New("installation record plan does not match its identity")
	}
	if r.State == InstallationUninstalled && r.LastOperationKind != Uninstall {
		return errors.New("uninstalled record is not bound to an uninstall operation")
	}
	if r.State == InstallationActive && (r.LastOperationKind == Backup || r.LastOperationKind == Uninstall) {
		return errors.New("active record is not bound to a version-changing operation")
	}
	return nil
}

func readInstallation(filename string) (InstallationRecord, error) {
	contents, err := readRegularFile(filename, 2<<20)
	if err != nil {
		return InstallationRecord{}, err
	}
	var record InstallationRecord
	decoder := json.NewDecoder(strings.NewReader(string(contents)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return InstallationRecord{}, errors.New("installation record is invalid")
	}
	if err := record.Validate(); err != nil {
		return InstallationRecord{}, err
	}
	return record, nil
}
