package plan

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPreflightRehashesVerifiedArtifact(t *testing.T) {
	candidate, err := DecodeAndValidate([]byte(validPlan))
	if err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(t.TempDir(), "bundle")
	contents := make([]byte, 42)
	if err := os.WriteFile(artifact, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	candidate.Release.ArtifactSHA256 = "094c4931fdb2f2af417c9e0322a9716006e8211fe9017f671ac6e3251300acca"
	report := Preflight(candidate, artifact)
	if !report.Ready {
		t.Fatalf("expected ready preflight: %+v", report)
	}
	if err := os.WriteFile(artifact, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if report := Preflight(candidate, artifact); report.Ready {
		t.Fatal("tampered artifact passed preflight")
	}
}

func TestDryRunNeverReportsMutation(t *testing.T) {
	candidate, err := DecodeAndValidate([]byte(validPlan))
	if err != nil {
		t.Fatal(err)
	}
	report, err := DryRun(candidate)
	if err != nil {
		t.Fatal(err)
	}
	if report.Mutated || len(report.Steps) == 0 {
		t.Fatalf("unexpected dry run report: %+v", report)
	}
	for _, step := range report.Steps {
		if step.Mutates {
			t.Fatalf("dry-run step reports mutation: %+v", step)
		}
	}
}
