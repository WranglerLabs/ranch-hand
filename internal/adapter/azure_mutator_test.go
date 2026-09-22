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

func azureEvaluationPlan() plan.DeploymentPlan {
	return targetPlan("azure-container-apps", map[string]string{
		"subscriptionId": "00000000-0000-0000-0000-000000000000", "resourceGroup": "rg-ranch-hand", "location": "eastus",
		"environmentName": "cae-ranch-hand", "appName": "repo-wrangler", "demoMode": "false", "postgresServerName": "repo-wrangler-prod-unique",
	})
}

func stagedAzureBundle(t *testing.T) bundle.StagedBundle {
	t.Helper()
	directory := t.TempDir()
	image := "ghcr.io/wranglerlabs/repo-wrangler-server@sha256:" + strings.Repeat("a", 64)
	identity := `{"schemaVersion":"1.0","product":"RepoWrangler","version":"v1.2.3","targetFamily":"azure-container-apps","image":"` + image + `","publicHttps":"azure-managed-ingress","registryAuthentication":"none-for-public-ghcr"}`
	if err := os.WriteFile(filepath.Join(directory, "bundle.json"), []byte(identity), 0o600); err != nil {
		t.Fatal(err)
	}
	template := `{"$schema":"https://schema.management.azure.com/schemas/2019-04-01/deploymentTemplate.json#","contentVersion":"1.0.0.0","parameters":{"provisionPostgres":{},"postgresServerName":{},"postgresAdminPassword":{},"sessionSecret":{},"secretEncryptionKey":{},"setupToken":{}},"resources":[]}`
	if err := os.WriteFile(filepath.Join(directory, "main.json"), []byte(template), 0o600); err != nil {
		t.Fatal(err)
	}
	return bundle.StagedBundle{Product: "RepoWrangler", Version: "v1.2.3", Target: "azure-container-apps", Path: directory}
}

func TestAzureProductionInstallDeploysPostgresTemplateAndChecksIdentity(t *testing.T) {
	var groupCreated, deploymentStarted bool
	image := "ghcr.io/wranglerlabs/repo-wrangler-server@sha256:" + strings.Repeat("a", 64)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer azure-token" {
			t.Fatal("ARM mutation omitted the in-memory bearer token")
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && strings.Contains(strings.ToLower(r.URL.Path), "/resourcegroups/rg-ranch-hand") && !strings.Contains(r.URL.Path, "/providers/"):
			http.Error(w, "missing", http.StatusNotFound)
		case r.Method == http.MethodPut && strings.HasSuffix(strings.ToLower(r.URL.Path), "/resourcegroups/rg-ranch-hand"):
			var payload struct {
				Tags map[string]string `json:"tags"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.Tags["wranglerlabs-ranch-hand-managed"] != "true" {
				t.Fatal("dedicated resource group omitted ownership tags")
			}
			groupCreated = true
			_, _ = io.WriteString(w, `{}`)
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/providers/Microsoft.Resources/deployments/"):
			var payload struct {
				Properties struct {
					Template   map[string]any            `json:"template"`
					Parameters map[string]map[string]any `json:"parameters"`
				} `json:"properties"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload.Properties.Template["$schema"] == nil || payload.Properties.Parameters["image"]["value"] != image || payload.Properties.Parameters["demoMode"]["value"] != false || payload.Properties.Parameters["postgres"]["value"] != true || payload.Properties.Parameters["provisionPostgres"]["value"] != true || payload.Properties.Parameters["postgresServerName"]["value"] != "repo-wrangler-prod-unique" || payload.Properties.Parameters["sessionSecret"]["value"] == "" || payload.Properties.Parameters["secretEncryptionKey"]["value"] == "" || payload.Properties.Parameters["setupToken"]["value"] == "" {
				t.Fatal("ARM deployment did not bind the verified production PostgreSQL and secret template")
			}
			deploymentStarted = true
			_, _ = io.WriteString(w, `{}`)
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/providers/Microsoft.Resources/deployments/"):
			_, _ = io.WriteString(w, `{"properties":{"provisioningState":"Succeeded"}}`)
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/providers/Microsoft.App/containerApps/repo-wrangler"):
			_, _ = io.WriteString(w, `{"properties":{"provisioningState":"Succeeded","configuration":{"ingress":{"fqdn":"repo-wrangler.kind.azurecontainerapps.io"}},"template":{"containers":[{"name":"server","image":"`+image+`"}]}}}`)
		default:
			t.Fatalf("unexpected ARM request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()
	adapter := newAzureContainerApps(server.Client(), server.URL)
	adapter.verifyPublicImage = func(context.Context, string) error { return nil }
	adapter.healthClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "https" || !strings.HasSuffix(r.URL.Host, ".azurecontainerapps.io") {
			t.Fatalf("health verification escaped Azure-managed HTTPS: %s", r.URL)
		}
		body := `{"ok":true,"demoMode":false}`
		if r.URL.Path == "/health/live" {
			body = `{"ok":true,"version":"v1.2.3"}`
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	candidate := azureEvaluationPlan()
	credentials := Credentials{AzureAccessToken: "azure-token", SetupToken: "abcdefghijklmnopqrstuvwxyz012345"}
	if err := adapter.Apply(context.Background(), lifecycle.Install, candidate, "", stagedAzureBundle(t), lifecycle.OperationBackups{}, credentials); err != nil {
		t.Fatal(err)
	}
	if !groupCreated || !deploymentStarted {
		t.Fatal("Azure production deployment did not create its owned boundary and ARM deployment")
	}
	if err := adapter.Verify(context.Background(), candidate, credentials); err != nil {
		t.Fatal(err)
	}
}

func TestAzureInstallRecoveryDeletesOnlyOwnedResourceGroup(t *testing.T) {
	candidate := azureEvaluationPlan()
	deploymentID, err := lifecycle.DeploymentID(candidate)
	if err != nil {
		t.Fatal(err)
	}
	deleted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			if deleted {
				http.Error(w, "missing", http.StatusNotFound)
				return
			}
			_, _ = io.WriteString(w, `{"tags":{"wranglerlabs-ranch-hand-managed":"true","wranglerlabs-ranch-hand-deployment":"`+deploymentID+`"}}`)
		case http.MethodDelete:
			deleted = true
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	defer server.Close()
	adapter := newAzureContainerApps(server.Client(), server.URL)
	if err := adapter.Recover(context.Background(), lifecycle.Install, candidate, "", lifecycle.OperationBackups{}, Credentials{AzureAccessToken: "azure-token"}); err != nil {
		t.Fatal(err)
	}
	if !deleted {
		t.Fatal("owned failed-install resource group was not deleted")
	}
}

func TestAzureUninstallDeletesOnlyExactOwnedResourceGroup(t *testing.T) {
	candidate := azureEvaluationPlan()
	deploymentID, err := lifecycle.DeploymentID(candidate)
	if err != nil {
		t.Fatal(err)
	}
	deleted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodDelete {
			deleted = true
			w.WriteHeader(http.StatusAccepted)
			return
		}
		if deleted {
			http.Error(w, "missing", http.StatusNotFound)
			return
		}
		_, _ = io.WriteString(w, `{"tags":{"wranglerlabs-ranch-hand-managed":"true","wranglerlabs-ranch-hand-deployment":"`+deploymentID+`","wranglerlabs-ranch-hand-version":"v1.2.3"}}`)
	}))
	defer server.Close()
	adapter := newAzureContainerApps(server.Client(), server.URL)
	if err := adapter.Apply(context.Background(), lifecycle.Uninstall, candidate, candidate.Release.Version, bundle.StagedBundle{}, lifecycle.OperationBackups{}, Credentials{AzureAccessToken: "azure-token"}); err != nil {
		t.Fatal(err)
	}
	if !deleted {
		t.Fatal("owned Azure resource group was not uninstalled")
	}
}

func TestAzureInstallRecoveryRefusesUnownedResourceGroup(t *testing.T) {
	deleted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			deleted = true
			w.WriteHeader(http.StatusAccepted)
			return
		}
		_, _ = io.WriteString(w, `{"tags":{"owner":"someone-else"}}`)
	}))
	defer server.Close()
	adapter := newAzureContainerApps(server.Client(), server.URL)
	err := adapter.Recover(context.Background(), lifecycle.Install, azureEvaluationPlan(), "", lifecycle.OperationBackups{}, Credentials{AzureAccessToken: "azure-token"})
	if err == nil || deleted {
		t.Fatal("Azure recovery deleted or accepted an unowned resource group")
	}
}
