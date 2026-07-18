package adapter

import (
	"strings"
	"testing"
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
