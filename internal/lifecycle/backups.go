package lifecycle

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const BackupSchemaVersion = "1.0"

type BackupKind string

const (
	LocalArchive       BackupKind = "local-archive"
	AzureSnapshot      BackupKind = "azure-snapshot"
	CloudflareD1Export BackupKind = "cloudflare-d1-export"
	RemoteArchive      BackupKind = "remote-archive"
)

var validBackupKinds = map[BackupKind]bool{LocalArchive: true, AzureSnapshot: true, CloudflareD1Export: true, RemoteArchive: true}
var validBackupTargets = map[string]bool{"azure-container-apps": true, "cloudflare": true, "local-compose": true, "remote-linux-compose": true}

type BackupArtifact struct {
	Kind    BackupKind `json:"kind"`
	Locator string     `json:"locator"`
	Size    int64      `json:"size,omitempty"`
	SHA256  string     `json:"sha256,omitempty"`
}

type BackupRecord struct {
	SchemaVersion string         `json:"schemaVersion"`
	BackupID      string         `json:"backupId"`
	DeploymentID  string         `json:"deploymentId"`
	OperationID   string         `json:"operationId"`
	Target        string         `json:"target"`
	Version       string         `json:"version"`
	CreatedAt     time.Time      `json:"createdAt"`
	Artifact      BackupArtifact `json:"artifact"`
}

func (s *Store) RecordBackup(journal Journal, artifact BackupArtifact) (BackupRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := artifact.Validate(); err != nil {
		return BackupRecord{}, err
	}
	if journal.Kind != Update && journal.Kind != Backup && journal.Kind != Repair {
		return BackupRecord{}, fmt.Errorf("%s operation cannot record a backup", journal.Kind)
	}
	directory := s.deploymentDirectory(journal.DeploymentID)
	if err := rejectSymlinkComponents(s.root, directory); err != nil {
		return BackupRecord{}, err
	}
	active, err := s.readActive(filepath.Join(directory, "active"), journal.DeploymentID)
	if err != nil {
		return BackupRecord{}, err
	}
	if active.OperationID != journal.OperationID || active.Phase != Prepared {
		return BackupRecord{}, errors.New("backup can only be recorded for the active operation in prepared phase")
	}
	backupID, err := randomID()
	if err != nil {
		return BackupRecord{}, err
	}
	record := BackupRecord{
		SchemaVersion: BackupSchemaVersion, BackupID: backupID, DeploymentID: journal.DeploymentID,
		OperationID: journal.OperationID, Target: journal.Target, Version: journal.FromVersion,
		CreatedAt: s.now().UTC(), Artifact: artifact,
	}
	if err := record.Validate(); err != nil {
		return BackupRecord{}, err
	}
	backupDirectory := filepath.Join(directory, "backups")
	if err := os.MkdirAll(backupDirectory, 0o700); err != nil {
		return BackupRecord{}, err
	}
	if err := atomicWrite(filepath.Join(backupDirectory, backupID+".json"), record); err != nil {
		return BackupRecord{}, err
	}
	return record, nil
}

func (s *Store) Backup(deploymentID, backupID string) (BackupRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !idPattern.MatchString(deploymentID) || !idPattern.MatchString(backupID) {
		return BackupRecord{}, errors.New("invalid backup identifier")
	}
	directory := s.deploymentDirectory(deploymentID)
	if err := rejectSymlinkComponents(s.root, directory); err != nil {
		return BackupRecord{}, err
	}
	return readBackup(filepath.Join(directory, "backups", backupID+".json"))
}

func (s *Store) Backups(deploymentID string) ([]BackupRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !idPattern.MatchString(deploymentID) {
		return nil, errors.New("invalid deployment identifier")
	}
	directory := s.deploymentDirectory(deploymentID)
	if err := rejectSymlinkComponents(s.root, directory); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(directory, "backups"))
	if errors.Is(err, os.ErrNotExist) {
		return []BackupRecord{}, nil
	}
	if err != nil {
		return nil, err
	}
	result := make([]BackupRecord, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		record, err := readBackup(filepath.Join(directory, "backups", entry.Name()))
		if err != nil {
			return nil, err
		}
		if record.DeploymentID != deploymentID {
			return nil, errors.New("backup record belongs to a different deployment")
		}
		if entry.Name() != record.BackupID+".json" {
			return nil, errors.New("backup record filename does not match its identity")
		}
		result = append(result, record)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.After(result[j].CreatedAt) })
	return result, nil
}

func (a BackupArtifact) Validate() error {
	if !validBackupKinds[a.Kind] {
		return fmt.Errorf("unsupported backup kind %q", a.Kind)
	}
	locator := strings.TrimSpace(a.Locator)
	lower := strings.ToLower(locator)
	if locator == "" || len(locator) > 1024 || strings.ContainsAny(locator, "\x00\r\n?#") || strings.Contains(lower, "://") || strings.Contains(lower, "token=") || strings.Contains(lower, "sig=") || strings.Contains(lower, "password=") || strings.Contains(lower, "secret=") {
		return errors.New("backup locator must be a credential-free local path or target resource identifier")
	}
	if strings.Contains(locator, "\\") || path.Clean(locator) != locator || strings.Contains(locator, "../") {
		return errors.New("backup locator is not a canonical portable path or resource identifier")
	}
	switch a.Kind {
	case LocalArchive, CloudflareD1Export:
		if path.IsAbs(locator) || !strings.HasPrefix(locator, "backups/") {
			return errors.New("local backup locator must be relative to the dedicated backups directory")
		}
	case RemoteArchive:
		if !path.IsAbs(locator) {
			return errors.New("remote archive locator must be an absolute POSIX path")
		}
	case AzureSnapshot:
		if !strings.HasPrefix(strings.ToLower(locator), "/subscriptions/") {
			return errors.New("Azure snapshot locator must be an Azure resource identifier")
		}
	}
	if a.Kind == LocalArchive || a.Kind == CloudflareD1Export || a.Kind == RemoteArchive {
		if a.Size < 1 || !digestPattern.MatchString(a.SHA256) {
			return errors.New("archive backup requires a positive size and lowercase SHA-256")
		}
	} else if a.Size != 0 || a.SHA256 != "" {
		return errors.New("target-native snapshot must not declare local archive size or digest")
	}
	return nil
}

func (r BackupRecord) Validate() error {
	if r.SchemaVersion != BackupSchemaVersion || !idPattern.MatchString(r.BackupID) || !idPattern.MatchString(r.DeploymentID) || !idPattern.MatchString(r.OperationID) || !validBackupTargets[r.Target] || r.CreatedAt.IsZero() || !versionPattern.MatchString(r.Version) {
		return errors.New("backup record identity is invalid")
	}
	if err := r.Artifact.Validate(); err != nil {
		return err
	}
	expectedKind := map[string]BackupKind{
		"azure-container-apps": AzureSnapshot,
		"cloudflare":           CloudflareD1Export,
		"local-compose":        LocalArchive,
		"remote-linux-compose": RemoteArchive,
	}[r.Target]
	if r.Artifact.Kind != expectedKind {
		return fmt.Errorf("backup kind %q does not match target %q", r.Artifact.Kind, r.Target)
	}
	return nil
}

func readBackup(filename string) (BackupRecord, error) {
	contents, err := readRegularFile(filename, 64<<10)
	if err != nil {
		return BackupRecord{}, err
	}
	var record BackupRecord
	decoder := json.NewDecoder(strings.NewReader(string(contents)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return BackupRecord{}, errors.New("backup record is invalid")
	}
	if err := record.Validate(); err != nil {
		return BackupRecord{}, err
	}
	return record, nil
}
