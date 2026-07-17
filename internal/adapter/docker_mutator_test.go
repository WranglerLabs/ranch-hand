package adapter

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/WranglerLabs/ranch-hand/internal/bundle"
	"github.com/WranglerLabs/ranch-hand/internal/lifecycle"
	"github.com/WranglerLabs/ranch-hand/internal/plan"
)

func localInstallPlan(dataDirectory string) plan.DeploymentPlan {
	return plan.DeploymentPlan{
		SchemaVersion: plan.CurrentSchemaVersion, Name: "Local Wrangler",
		Release: plan.ReleaseSelection{
			Version: "v1.2.3", ManifestURL: "https://github.com/WranglerLabs/repo-wrangler/releases/download/v1.2.3/release-manifest.json",
			ManifestSHA256: strings.Repeat("a", 64), ArtifactSHA256: strings.Repeat("b", 64), ArtifactSize: 42,
		},
		Target: plan.Target{Kind: "local-compose"},
		Configuration: map[string]string{
			"projectName": "repo-wrangler", "dataDirectory": dataDirectory, "listenAddress": "127.0.0.1:18080",
		},
	}
}

func stagedComposeBundle(t *testing.T) bundle.StagedBundle {
	t.Helper()
	directory := t.TempDir()
	image := "ghcr.io/wranglerlabs/repo-wrangler-server@sha256:" + strings.Repeat("a", 64)
	postgres := "docker.io/library/postgres@sha256:" + strings.Repeat("b", 64)
	identity := `{"schemaVersion":"1.0","product":"RepoWrangler","version":"v1.2.3","targetFamily":"compose","image":"` + image + `","postgresImage":"` + postgres + `","publicHttps":"operator-provided","defaultBindAddress":"127.0.0.1"}`
	for name, contents := range map[string]string{"bundle.json": identity, "compose.yaml": "services: {}\n", ".env.example": "DEMO_MODE=true\n"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return bundle.StagedBundle{Product: "RepoWrangler", Version: "v1.2.3", Target: "local-compose", Path: directory}
}

func TestLocalDockerInstallUsesEngineAPI(t *testing.T) {
	var created map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/containers/repo-wrangler-server/json":
			http.Error(w, "missing", http.StatusNotFound)
		case r.Method == http.MethodPost && r.URL.Path == "/images/create":
			if !strings.Contains(r.URL.Query().Get("fromImage"), "@sha256:") {
				t.Fatal("image pull was not digest-pinned")
			}
			_, _ = io.WriteString(w, "{\"status\":\"done\"}\n")
		case r.Method == http.MethodPost && r.URL.Path == "/containers/create":
			if r.URL.Query().Get("name") != "repo-wrangler-server" {
				t.Fatal("unexpected container name")
			}
			if err := json.NewDecoder(r.Body).Decode(&created); err != nil {
				t.Fatal(err)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"Id":"container-id"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/containers/container-id/start":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected Docker request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()
	dataDirectory := filepath.Join(t.TempDir(), "data")
	adapter := &LocalDocker{client: server.Client(), baseURL: server.URL}
	err := adapter.Apply(context.Background(), lifecycle.Install, localInstallPlan(dataDirectory), stagedComposeBundle(t), Credentials{})
	if err != nil {
		t.Fatal(err)
	}
	if created["Image"] == "" {
		t.Fatal("container create request omitted image")
	}
	labels := created["Labels"].(map[string]any)
	if labels["com.wranglerlabs.ranch-hand.managed"] != "true" {
		t.Fatal("container create request omitted ownership label")
	}
	if details, err := os.Stat(dataDirectory); err != nil || !details.IsDir() {
		t.Fatalf("data directory was not created: %v", err)
	}
}

func TestLocalDockerVerifyUsesLoopbackHealth(t *testing.T) {
	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health/ready" {
			t.Fatal("unexpected health path")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer health.Close()
	adapter := &LocalDocker{healthClient: health.Client(), healthURL: func(plan.DeploymentPlan) (string, error) { return health.URL + "/health/ready", nil }}
	if err := adapter.Verify(context.Background(), localInstallPlan(filepath.Join(t.TempDir(), "data")), Credentials{}); err != nil {
		t.Fatal(err)
	}
}

func TestLocalDockerRecoveryDeletesOnlyOwnedContainer(t *testing.T) {
	candidate := localInstallPlan(filepath.Join(t.TempDir(), "data"))
	deploymentID, err := lifecycle.DeploymentID(candidate)
	if err != nil {
		t.Fatal(err)
	}
	deleted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "owned-id", "Config": map[string]any{"Labels": map[string]string{"com.wranglerlabs.ranch-hand.managed": "true", "com.wranglerlabs.ranch-hand.deployment": deploymentID}}})
		case http.MethodDelete:
			if r.URL.Path != "/containers/owned-id" {
				t.Fatal("recovery deleted unexpected container")
			}
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	adapter := &LocalDocker{client: server.Client(), baseURL: server.URL}
	if err := adapter.Recover(context.Background(), lifecycle.Install, candidate, nil, Credentials{}); err != nil {
		t.Fatal(err)
	}
	if !deleted {
		t.Fatal("owned partial container was not removed")
	}
}

func TestLocalDockerRecoveryRefusesUnownedContainer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"Id": "unowned", "Config": map[string]any{"Labels": map[string]string{}}})
	}))
	defer server.Close()
	adapter := &LocalDocker{client: server.Client(), baseURL: server.URL}
	if err := adapter.Recover(context.Background(), lifecycle.Install, localInstallPlan(filepath.Join(t.TempDir(), "data")), nil, Credentials{}); err == nil {
		t.Fatal("recovery removed or accepted an unowned container")
	}
}

func TestLocalDockerRejectsTrailingJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"Id":"first","Config":{"Labels":{}}}{"Id":"second"}`)
	}))
	defer server.Close()
	adapter := &LocalDocker{client: server.Client(), baseURL: server.URL}
	if _, _, _, err := adapter.containerMetadata(context.Background(), "repo-wrangler-server"); err == nil {
		t.Fatal("Docker response with trailing JSON was accepted")
	}
}
