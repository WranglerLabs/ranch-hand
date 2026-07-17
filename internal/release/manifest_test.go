package release

import "testing"

func TestWSLUsesPublishedComposeArtifact(t *testing.T) {
	if err := ValidateTarget("local-wsl-compose"); err != nil {
		t.Fatalf("WSL target rejected: %v", err)
	}
	if target := ArtifactTarget("local-wsl-compose"); target != "local-compose" {
		t.Fatalf("WSL target mapped to %q", target)
	}
}
