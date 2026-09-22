package adapter

import (
	"context"
	"strings"
	"testing"

	"github.com/WranglerLabs/ranch-hand/internal/bundle"
	"github.com/WranglerLabs/ranch-hand/internal/lifecycle"
)

func TestWSLBoundaryMessagesAreLocalAndActionable(t *testing.T) {
	for _, test := range []struct {
		err  error
		want string
	}{
		{errComposeInstallDirectoryExists, "local WSL installation directory"},
		{errComposeContainersExist, `local WSL Compose project "repo-wrangler-ranch-hand" already has containers`},
		{errComposeVolumesExist, `local WSL Compose project "repo-wrangler-ranch-hand" already has volumes`},
	} {
		message := wslBoundaryMessage("repo-wrangler-ranch-hand", test.err)
		if !strings.Contains(message, test.want) || strings.Contains(message, "remote") {
			t.Fatalf("unexpected WSL boundary message %q", message)
		}
	}
}

func TestWSLApplyValidatesDistributionBeforeOperatingSystemCommand(t *testing.T) {
	candidate := targetPlan("local-wsl-compose", map[string]string{
		"distribution": "Ubuntu\nattacker", "projectName": "repo-wrangler",
	})
	err := NewWSLCompose().Apply(context.Background(), lifecycle.Install, candidate, "", bundle.StagedBundle{}, lifecycle.OperationBackups{}, Credentials{})
	if err == nil || !strings.Contains(err.Error(), "distribution") {
		t.Fatalf("unsafe WSL distribution reached apply: %v", err)
	}
}

func TestWSLNormalizationPreservesLegacyPlanIdentityWithoutEmptyDemoField(t *testing.T) {
	candidate := targetPlan("local-wsl-compose", map[string]string{
		"distribution": "Ubuntu", "projectName": "repo-wrangler",
	})
	normalized, err := normalizeWSLPlan(context.Background(), candidate, newFakeRemoteHost())
	if err != nil {
		t.Fatal(err)
	}
	if _, present := normalized.Configuration["demoMode"]; present {
		t.Fatal("legacy WSL plan gained an empty demoMode field and changed identity")
	}
}
