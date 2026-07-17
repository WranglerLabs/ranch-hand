package diagnostics

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/WranglerLabs/ranch-hand/internal/lifecycle"
)

type fakeSource struct {
	installations []lifecycle.InstallationRecord
	backups       []lifecycle.BackupRecord
	active        lifecycle.Journal
}

func (f fakeSource) Installations() ([]lifecycle.InstallationRecord, error) {
	return f.installations, nil
}
func (f fakeSource) Backups(string) ([]lifecycle.BackupRecord, error) { return f.backups, nil }
func (f fakeSource) Active(string) (lifecycle.Journal, error) {
	if f.active.OperationID == "" {
		return lifecycle.Journal{}, os.ErrNotExist
	}
	return f.active, nil
}

func TestCollectRedactsPlansLocatorsAndConfigurationValues(t *testing.T) {
	now := time.Date(2026, 7, 17, 8, 0, 0, 0, time.UTC)
	source := fakeSource{
		installations: []lifecycle.InstallationRecord{{
			DeploymentID: strings.Repeat("a", 24), Target: "cloudflare", State: lifecycle.InstallationActive,
			Version: "v1.2.3", PlanSHA256: strings.Repeat("b", 64), Plan: json.RawMessage(`{"configuration":{"accountId":"sensitive-account","customDomain":"private.example"}}`),
			InstalledAt: now.Add(-time.Hour), UpdatedAt: now, LastOperationID: strings.Repeat("c", 32), LastOperationKind: lifecycle.Install, LastEventHash: strings.Repeat("d", 64),
		}},
		backups: []lifecycle.BackupRecord{{
			BackupID: strings.Repeat("e", 32), OperationID: strings.Repeat("f", 32), Version: "v1.2.3", CreatedAt: now,
			Artifact: lifecycle.BackupArtifact{Kind: lifecycle.CloudflareD1Export, Locator: "backups/private-account-export.sql", Size: 42, SHA256: strings.Repeat("1", 64)},
		}},
		active: lifecycle.Journal{OperationID: strings.Repeat("2", 32), Kind: lifecycle.Restore, FromVersion: "v1.2.3", ToVersion: "v1.2.3", Phase: lifecycle.Staged, UpdatedAt: now, InputBackupID: strings.Repeat("3", 32), Plan: json.RawMessage(`{"host":"internal.example"}`)},
	}
	snapshot, err := (Collector{Now: func() time.Time { return now }, Random: bytes.NewReader(make([]byte, 32))}).Collect("v0.1.0-rc.1", source)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, forbidden := range []string{"sensitive-account", "private.example", "private-account-export", "internal.example", strings.Repeat("a", 24), "configuration", "locator", "plansha", "plan\""} {
		if strings.Contains(strings.ToLower(text), strings.ToLower(forbidden)) {
			t.Fatalf("diagnostics leaked %q: %s", forbidden, text)
		}
	}
	if !strings.Contains(text, `"inputBackupId"`) || !strings.Contains(text, `"sha256"`) {
		t.Fatalf("diagnostics omitted safe integrity evidence: %s", text)
	}
}

func TestCollectFailsClosedWhenLifecycleInventoryIsUnreadable(t *testing.T) {
	source := failingSource{}
	if _, err := (Collector{}).Collect("test", source); err == nil {
		t.Fatal("diagnostics silently omitted an unreadable lifecycle inventory")
	}
}

type failingSource struct{}

func (failingSource) Installations() ([]lifecycle.InstallationRecord, error) {
	return nil, errors.New("corrupt inventory")
}
func (failingSource) Backups(string) ([]lifecycle.BackupRecord, error) { return nil, nil }
func (failingSource) Active(string) (lifecycle.Journal, error)         { return lifecycle.Journal{}, nil }
