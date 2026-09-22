package lifecycle

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/WranglerLabs/ranch-hand/internal/plan"
)

const (
	JournalSchemaVersion       = "1.1"
	legacyJournalSchemaVersion = "1.0"
)

type OperationKind string

const (
	Install   OperationKind = "install"
	Update    OperationKind = "update"
	Backup    OperationKind = "backup"
	Restore   OperationKind = "restore"
	Repair    OperationKind = "repair"
	Rollback  OperationKind = "rollback"
	Uninstall OperationKind = "uninstall"
)

type Phase string

const (
	Prepared        Phase = "prepared"
	BackupComplete  Phase = "backup-complete"
	Staged          Phase = "staged"
	Applied         Phase = "applied"
	Verified        Phase = "verified"
	Committed       Phase = "committed"
	RecoveryStarted Phase = "recovery-started"
	Recovered       Phase = "recovered"
	Failed          Phase = "failed"
)

type Event struct {
	Sequence     int       `json:"sequence"`
	Phase        Phase     `json:"phase"`
	RecordedAt   time.Time `json:"recordedAt"`
	PreviousHash string    `json:"previousHash,omitempty"`
	ReferenceID  string    `json:"referenceId,omitempty"`
	Hash         string    `json:"hash"`
}

type Journal struct {
	SchemaVersion string          `json:"schemaVersion"`
	OperationID   string          `json:"operationId"`
	DeploymentID  string          `json:"deploymentId"`
	Kind          OperationKind   `json:"kind"`
	Target        string          `json:"target"`
	FromVersion   string          `json:"fromVersion,omitempty"`
	ToVersion     string          `json:"toVersion"`
	InputBackupID string          `json:"inputBackupId,omitempty"`
	PlanSHA256    string          `json:"planSha256"`
	Plan          json.RawMessage `json:"plan"`
	StartedAt     time.Time       `json:"startedAt"`
	UpdatedAt     time.Time       `json:"updatedAt"`
	Phase         Phase           `json:"phase"`
	Events        []Event         `json:"events"`
}

type Store struct {
	root string
	now  func() time.Time
	mu   sync.Mutex
}

var (
	idPattern      = regexp.MustCompile(`^[a-f0-9]{24,64}$`)
	digestPattern  = regexp.MustCompile(`^[a-f0-9]{64}$`)
	versionPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(?:[-+][A-Za-z0-9.-]+)?$`)
	validKinds     = map[OperationKind]bool{Install: true, Update: true, Backup: true, Restore: true, Repair: true, Rollback: true, Uninstall: true}
	transitions    = map[Phase]map[Phase]bool{
		Prepared:        {BackupComplete: true, Staged: true, Applied: true, Failed: true},
		BackupComplete:  {Staged: true, Applied: true, Committed: true, Failed: true},
		Staged:          {Applied: true, RecoveryStarted: true, Failed: true},
		Applied:         {Verified: true, RecoveryStarted: true, Failed: true},
		Verified:        {Committed: true, RecoveryStarted: true, Failed: true},
		RecoveryStarted: {Recovered: true, Failed: true},
	}
)

func NewStore(root string) (*Store, error) {
	if root == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return nil, fmt.Errorf("locate user configuration: %w", err)
		}
		root = filepath.Join(base, "WranglerLabs", "Ranch Hand", "state")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve lifecycle state root: %w", err)
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return nil, fmt.Errorf("create lifecycle state root: %w", err)
	}
	physical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, fmt.Errorf("resolve physical lifecycle state root: %w", err)
	}
	return &Store{root: filepath.Clean(physical), now: func() time.Time { return time.Now().UTC() }}, nil
}

func DeploymentID(candidate plan.DeploymentPlan) (string, error) {
	if err := candidate.Validate(); err != nil {
		return "", err
	}
	identity := struct {
		Target        plan.Target       `json:"target"`
		Configuration map[string]string `json:"configuration"`
	}{Target: candidate.Target, Configuration: candidate.Configuration}
	encoded, err := json.Marshal(identity)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:12]), nil
}

func (s *Store) Begin(kind OperationKind, candidate plan.DeploymentPlan, fromVersion string) (Journal, error) {
	return s.BeginWithInputBackup(kind, candidate, fromVersion, "")
}

func (s *Store) BeginWithInputBackup(kind OperationKind, candidate plan.DeploymentPlan, fromVersion, inputBackupID string) (Journal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !validKinds[kind] {
		return Journal{}, fmt.Errorf("unsupported lifecycle operation %q", kind)
	}
	if err := candidate.Validate(); err != nil {
		return Journal{}, err
	}
	if err := validateOperationVersions(kind, fromVersion); err != nil {
		return Journal{}, err
	}
	if (kind == Restore || kind == Rollback) != idPattern.MatchString(inputBackupID) {
		return Journal{}, errors.New("restore and rollback require one valid input backup identity; other operations cannot declare one")
	}
	deploymentID, err := DeploymentID(candidate)
	if err != nil {
		return Journal{}, err
	}
	canonical, err := plan.CanonicalJSON(candidate)
	if err != nil {
		return Journal{}, err
	}
	planDigest := sha256.Sum256(canonical)
	operationID, err := randomID()
	if err != nil {
		return Journal{}, err
	}
	directory := s.deploymentDirectory(deploymentID)
	if err := rejectSymlinkComponents(s.root, directory); err != nil {
		return Journal{}, err
	}
	if err := os.MkdirAll(filepath.Join(directory, "operations"), 0o700); err != nil {
		return Journal{}, fmt.Errorf("create operation directory: %w", err)
	}
	activePath := filepath.Join(directory, "active")
	if existing, err := s.readActive(activePath, deploymentID); err == nil && !terminal(existing.Phase) {
		return Journal{}, fmt.Errorf("deployment already has active %s operation %s in phase %s", existing.Kind, existing.OperationID, existing.Phase)
	} else if err == nil && terminal(existing.Phase) {
		// A committed journal can survive a process interruption between its
		// durable write and installation-record/lock finalization. Reconcile that
		// state before allowing another operation to acquire the deployment.
		if existing.Phase == Committed && existing.Kind != Backup {
			if _, reconcileErr := s.recordInstallationLocked(existing); reconcileErr != nil {
				return Journal{}, fmt.Errorf("reconcile committed installation state: %w", reconcileErr)
			}
		}
		if removeErr := removeLock(activePath, existing.OperationID); removeErr != nil {
			return Journal{}, removeErr
		}
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Journal{}, err
	}
	if err := s.validateOperationCurrentStateLocked(kind, deploymentID, fromVersion); err != nil {
		return Journal{}, err
	}
	lock, err := os.OpenFile(activePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return Journal{}, errors.New("deployment lifecycle state is locked by another Ranch Hand process")
		}
		return Journal{}, fmt.Errorf("create lifecycle lock: %w", err)
	}
	lockCommitted := false
	defer func() {
		_ = lock.Close()
		if !lockCommitted {
			_ = os.Remove(activePath)
		}
	}()
	if _, err := io.WriteString(lock, operationID+"\n"); err != nil || lock.Sync() != nil || lock.Close() != nil {
		return Journal{}, errors.New("persist lifecycle lock")
	}
	started := s.now().UTC()
	journal := Journal{
		SchemaVersion: JournalSchemaVersion, OperationID: operationID, DeploymentID: deploymentID,
		Kind: kind, Target: candidate.Target.Kind, FromVersion: fromVersion, ToVersion: candidate.Release.Version,
		InputBackupID: inputBackupID,
		PlanSHA256:    hex.EncodeToString(planDigest[:]), Plan: append(json.RawMessage(nil), canonical...), StartedAt: started, UpdatedAt: started,
	}
	journal.Events = append(journal.Events, makeEvent(1, Prepared, started, journalHeaderHash(journal), ""))
	journal.Phase = Prepared
	if err := atomicWrite(filepath.Join(directory, "operations", operationID+".json"), journal); err != nil {
		return Journal{}, err
	}
	if err := atomicWriteBytes(filepath.Join(directory, "operations", operationID+".plan.json"), canonical); err != nil {
		return Journal{}, err
	}
	lockCommitted = true
	return journal, nil
}

func (s *Store) Transition(deploymentID, operationID string, next Phase) (Journal, error) {
	return s.TransitionWithReference(deploymentID, operationID, next, "")
}

func (s *Store) TransitionWithReference(deploymentID, operationID string, next Phase, referenceID string) (Journal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !idPattern.MatchString(deploymentID) || !idPattern.MatchString(operationID) {
		return Journal{}, errors.New("invalid lifecycle identifier")
	}
	directory := s.deploymentDirectory(deploymentID)
	if err := rejectSymlinkComponents(s.root, directory); err != nil {
		return Journal{}, err
	}
	activeID, err := readSmallFile(filepath.Join(directory, "active"), 128)
	if err != nil {
		return Journal{}, fmt.Errorf("read lifecycle lock: %w", err)
	}
	if strings.TrimSpace(activeID) != operationID {
		return Journal{}, errors.New("operation does not own the deployment lifecycle lock")
	}
	journalPath := filepath.Join(directory, "operations", operationID+".json")
	journal, err := readJournal(journalPath)
	if err != nil {
		return Journal{}, err
	}
	if journal.Phase == Committed && next == Committed {
		if journal.Kind != Backup {
			if _, err := s.recordInstallationLocked(journal); err != nil {
				return journal, err
			}
		}
		if err := removeLock(filepath.Join(directory, "active"), operationID); err != nil {
			return journal, err
		}
		return journal, nil
	}
	if !transitions[journal.Phase][next] {
		return Journal{}, fmt.Errorf("invalid lifecycle transition from %s to %s", journal.Phase, next)
	}
	if next == BackupComplete && !idPattern.MatchString(referenceID) {
		return Journal{}, errors.New("backup-complete requires a valid backup reference")
	}
	if referenceID != "" && !idPattern.MatchString(referenceID) {
		return Journal{}, errors.New("lifecycle reference identifier is invalid")
	}
	if next == Committed {
		if err := validateCommitSequence(journal); err != nil {
			return Journal{}, err
		}
	}
	now := s.now().UTC()
	previousHash := journal.Events[len(journal.Events)-1].Hash
	journal.Events = append(journal.Events, makeEvent(len(journal.Events)+1, next, now, previousHash, referenceID))
	journal.Phase = next
	journal.UpdatedAt = now
	if err := atomicWrite(journalPath, journal); err != nil {
		return Journal{}, err
	}
	if next == Committed && journal.Kind != Backup {
		if _, err := s.recordInstallationLocked(journal); err != nil {
			// Keep the operation lock in place. Repeating the committed transition
			// or beginning a later operation will reconcile this exact journal.
			return journal, err
		}
	}
	if terminal(next) {
		if err := removeLock(filepath.Join(directory, "active"), operationID); err != nil {
			return Journal{}, err
		}
	}
	return journal, nil
}

func (s *Store) Active(deploymentID string) (Journal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !idPattern.MatchString(deploymentID) {
		return Journal{}, errors.New("invalid deployment identifier")
	}
	if err := rejectSymlinkComponents(s.root, s.deploymentDirectory(deploymentID)); err != nil {
		return Journal{}, err
	}
	return s.readActive(filepath.Join(s.deploymentDirectory(deploymentID), "active"), deploymentID)
}

func (s *Store) ActiveOperations() ([]Journal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	root := filepath.Join(s.root, "deployments")
	if err := rejectSymlinkComponents(s.root, root); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return []Journal{}, nil
	}
	if err != nil {
		return nil, err
	}
	active := make([]Journal, 0)
	for _, entry := range entries {
		if !idPattern.MatchString(entry.Name()) || !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		directory := filepath.Join(root, entry.Name())
		if err := rejectSymlinkComponents(s.root, directory); err != nil {
			return nil, err
		}
		journal, err := s.readActive(filepath.Join(directory, "active"), entry.Name())
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		active = append(active, journal)
	}
	sort.Slice(active, func(i, j int) bool { return active[i].UpdatedAt.After(active[j].UpdatedAt) })
	return active, nil
}

func (s *Store) readActive(activePath, deploymentID string) (Journal, error) {
	operationID, err := readSmallFile(activePath, 128)
	if err != nil {
		return Journal{}, err
	}
	operationID = strings.TrimSpace(operationID)
	if !idPattern.MatchString(operationID) {
		return Journal{}, errors.New("active lifecycle lock is corrupt")
	}
	return readJournal(filepath.Join(s.deploymentDirectory(deploymentID), "operations", operationID+".json"))
}

func (s *Store) deploymentDirectory(deploymentID string) string {
	return filepath.Join(s.root, "deployments", deploymentID)
}

func validateCommitSequence(journal Journal) error {
	seen := make(map[Phase]bool, len(journal.Events))
	for _, event := range journal.Events {
		seen[event.Phase] = true
	}
	switch journal.Kind {
	case Install:
		if !seen[Staged] || !seen[Applied] || !seen[Verified] {
			return errors.New("install cannot commit before staged, applied, and verified phases")
		}
	case Update:
		if !seen[BackupComplete] || !seen[Staged] || !seen[Applied] || !seen[Verified] {
			return errors.New("update cannot commit before backup, staged, applied, and verified phases")
		}
	case Backup:
		if !seen[BackupComplete] {
			return errors.New("backup cannot commit before backup-complete")
		}
	case Restore, Repair, Rollback:
		if !seen[BackupComplete] || !seen[Applied] || !seen[Verified] {
			return fmt.Errorf("%s cannot commit before backup, applied, and verified phases", journal.Kind)
		}
	case Uninstall:
		if !seen[Applied] {
			return errors.New("uninstall cannot commit before applied phase")
		}
	}
	return nil
}

func readJournal(filename string) (Journal, error) {
	contents, err := readRegularFile(filename, 2<<20)
	if err != nil {
		return Journal{}, err
	}
	var journal Journal
	decoder := json.NewDecoder(strings.NewReader(string(contents)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&journal); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return Journal{}, errors.New("lifecycle journal is invalid")
	}
	if err := journal.Validate(); err != nil {
		return Journal{}, err
	}
	return journal, nil
}

func (j Journal) Validate() error {
	if (j.SchemaVersion != JournalSchemaVersion && j.SchemaVersion != legacyJournalSchemaVersion) || !idPattern.MatchString(j.OperationID) || !idPattern.MatchString(j.DeploymentID) || !validKinds[j.Kind] || !digestPattern.MatchString(j.PlanSHA256) || len(j.Events) == 0 {
		return errors.New("lifecycle journal identity is invalid")
	}
	if err := validateOperationVersions(j.Kind, j.FromVersion); err != nil {
		return err
	}
	if j.SchemaVersion == legacyJournalSchemaVersion && (j.InputBackupID != "" || j.Kind == Restore || j.Kind == Rollback) {
		return errors.New("legacy lifecycle journal cannot declare a restore input")
	}
	if j.SchemaVersion == JournalSchemaVersion && (j.Kind == Restore || j.Kind == Rollback) != idPattern.MatchString(j.InputBackupID) {
		return errors.New("lifecycle journal input backup identity is invalid")
	}
	candidate, err := plan.DecodeAndValidate(j.Plan)
	if err != nil {
		return errors.New("lifecycle journal plan snapshot is invalid")
	}
	canonical, err := plan.CanonicalJSON(candidate)
	if err != nil {
		return errors.New("lifecycle journal plan snapshot is invalid")
	}
	planDigest := sha256.Sum256(canonical)
	deploymentID, err := DeploymentID(candidate)
	if err != nil || hex.EncodeToString(planDigest[:]) != j.PlanSHA256 || deploymentID != j.DeploymentID || candidate.Target.Kind != j.Target || candidate.Release.Version != j.ToVersion {
		return errors.New("lifecycle journal plan snapshot does not match its operation identity")
	}
	previous := journalHeaderHash(j)
	for index, event := range j.Events {
		if event.RecordedAt.IsZero() || (index > 0 && event.RecordedAt.Before(j.Events[index-1].RecordedAt)) {
			return errors.New("lifecycle journal event time is invalid")
		}
		if event.Sequence != index+1 || event.PreviousHash != previous || event.Hash != makeEvent(event.Sequence, event.Phase, event.RecordedAt, event.PreviousHash, event.ReferenceID).Hash {
			return errors.New("lifecycle journal event chain is invalid")
		}
		if index > 0 && !transitions[j.Events[index-1].Phase][event.Phase] {
			return errors.New("lifecycle journal contains an invalid phase transition")
		}
		if event.Phase == BackupComplete && !idPattern.MatchString(event.ReferenceID) {
			return errors.New("lifecycle backup event has no valid backup reference")
		}
		if event.ReferenceID != "" && !idPattern.MatchString(event.ReferenceID) {
			return errors.New("lifecycle event reference is invalid")
		}
		previous = event.Hash
	}
	last := j.Events[len(j.Events)-1]
	if j.Phase != last.Phase || !j.UpdatedAt.Equal(last.RecordedAt) || !j.StartedAt.Equal(j.Events[0].RecordedAt) {
		return errors.New("lifecycle journal timestamps or current phase are invalid")
	}
	if j.Phase == Committed {
		if err := validateCommitSequence(j); err != nil {
			return err
		}
	}
	return nil
}

func journalHeaderHash(journal Journal) string {
	payload := ""
	if journal.SchemaVersion == legacyJournalSchemaVersion {
		payload = fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s", journal.SchemaVersion, journal.OperationID, journal.DeploymentID, journal.Kind, journal.Target, journal.FromVersion, journal.ToVersion, journal.PlanSHA256)
	} else {
		payload = fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s", journal.SchemaVersion, journal.OperationID, journal.DeploymentID, journal.Kind, journal.Target, journal.FromVersion, journal.ToVersion, journal.InputBackupID, journal.PlanSHA256)
	}
	digest := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(digest[:])
}

func validateOperationVersions(kind OperationKind, fromVersion string) error {
	if kind == Install && fromVersion != "" {
		return errors.New("install cannot specify fromVersion")
	}
	if (kind == Update || kind == Restore || kind == Rollback || kind == Repair || kind == Uninstall || kind == Backup) && fromVersion == "" {
		return fmt.Errorf("%s requires an explicit fromVersion", kind)
	}
	if fromVersion != "" && !versionPattern.MatchString(fromVersion) {
		return errors.New("fromVersion must be an explicit immutable version")
	}
	return nil
}

func makeEvent(sequence int, phase Phase, recordedAt time.Time, previousHash, referenceID string) Event {
	payload := fmt.Sprintf("%d\x00%s\x00%s\x00%s\x00%s", sequence, phase, recordedAt.UTC().Format(time.RFC3339Nano), previousHash, referenceID)
	digest := sha256.Sum256([]byte(payload))
	return Event{Sequence: sequence, Phase: phase, RecordedAt: recordedAt.UTC(), PreviousHash: previousHash, ReferenceID: referenceID, Hash: hex.EncodeToString(digest[:])}
}

func randomID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func terminal(phase Phase) bool { return phase == Committed || phase == Recovered || phase == Failed }

func atomicWrite(filename string, value any) error {
	contents, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteBytes(filename, append(contents, '\n'))
}

func atomicWriteBytes(filename string, contents []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(filename), ".state-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if _, err := temporary.Write(contents); err != nil || temporary.Sync() != nil || temporary.Close() != nil {
		return errors.New("persist lifecycle state")
	}
	if err := replaceFile(temporaryPath, filename); err != nil {
		return err
	}
	committed = true
	return nil
}

func readSmallFile(filename string, maximum int64) (string, error) {
	contents, err := readRegularFile(filename, maximum)
	if err != nil {
		return "", err
	}
	return string(contents), nil
}

func readRegularFile(filename string, maximum int64) ([]byte, error) {
	details, err := os.Lstat(filename)
	if err != nil {
		return nil, err
	}
	if !details.Mode().IsRegular() || details.Size() > maximum {
		return nil, errors.New("lifecycle state file is not a bounded regular file")
	}
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(contents)) > maximum {
		return nil, errors.New("lifecycle state file exceeds safety limit")
	}
	return contents, nil
}

func removeLock(filename, operationID string) error {
	contents, err := readSmallFile(filename, 128)
	if err != nil {
		return err
	}
	if strings.TrimSpace(contents) != operationID {
		return errors.New("lifecycle lock ownership changed")
	}
	return os.Remove(filename)
}

func rejectSymlinkComponents(root, candidate string) error {
	relative, err := filepath.Rel(root, candidate)
	if err != nil || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return errors.New("lifecycle state path escapes its dedicated root")
	}
	current := root
	for _, component := range strings.Split(relative, string(os.PathSeparator)) {
		if component == "." || component == "" {
			continue
		}
		current = filepath.Join(current, component)
		details, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if details.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("lifecycle state path contains symbolic link %q", current)
		}
	}
	return nil
}
