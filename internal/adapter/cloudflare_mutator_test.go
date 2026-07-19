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

const cloudflareTestAccount = "0123456789abcdef0123456789abcdef"
const cloudflareTestDatabaseID = "11111111-2222-3333-4444-555555555555"

func cloudflareEvaluationPlan() plan.DeploymentPlan {
	return targetPlan("cloudflare", map[string]string{
		"accountId": cloudflareTestAccount, "workerName": "repo-wrangler", "databaseName": "repo-wrangler",
	})
}

func stagedCloudflareBundle(t *testing.T) bundle.StagedBundle {
	t.Helper()
	directory := t.TempDir()
	for _, name := range []string{"assets", "migrations"} {
		if err := os.Mkdir(filepath.Join(directory, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	identity := `{"schemaVersion":"1.0","product":"RepoWrangler","version":"v1.2.3","targetFamily":"cloudflare","worker":"worker.js","assetsDirectory":"assets","migrationsDirectory":"migrations","compatibilityDate":"2026-07-01","publicHttps":"cloudflare-managed","assetsBinding":"ASSETS","d1Binding":"DB","assetsNotFoundHandling":"single-page-application","assetsRunWorkerFirst":["/api/*","/auth/*","/webhooks/*","/health/*","/setup/*"],"crons":["*/5 * * * *","17 3 * * *"],"vars":{"ALLOWED_GITHUB_USERS":"","APP_VERSION":"v1.2.3","AUTH_MODE":"github_app","DEMO_MODE":"true"},"observabilityEnabled":true}`
	files := map[string]string{
		"bundle.json": identity, "worker.js": "export default { fetch() { return new Response('ok') } };\n",
		filepath.Join("assets", "index.html"):           "<h1>RepoWrangler</h1>\n",
		filepath.Join("migrations", "0001_initial.sql"): "CREATE TABLE example (id INTEGER PRIMARY KEY);\n",
	}
	for name, contents := range files {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return bundle.StagedBundle{Product: "RepoWrangler", Version: "v1.2.3", Target: "cloudflare", Path: directory}
}

func writeCloudflareResult(w http.ResponseWriter, result string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"success":true,"result":`+result+`}`)
}

func TestCloudflareEvaluationInstallUsesNativeAPIsAndVerifiesIdentity(t *testing.T) {
	var databaseCreated, markerWritten, migrationApplied, assetsUploaded, workerUploaded, schedulesUpdated, subdomainEnabled bool
	var assetHash string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			t.Fatal("Cloudflare request omitted authorization")
		}
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/workers/scripts/repo-wrangler") && !strings.HasSuffix(r.URL.Path, "/settings") && !strings.HasSuffix(r.URL.Path, "/subdomain") && !strings.HasSuffix(r.URL.Path, "/schedules"):
			http.Error(w, "missing", http.StatusNotFound)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/d1/database"):
			if databaseCreated {
				writeCloudflareResult(w, `[{"name":"repo-wrangler","uuid":"`+cloudflareTestDatabaseID+`"}]`)
			} else {
				writeCloudflareResult(w, `[]`)
			}
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/d1/database"):
			databaseCreated = true
			writeCloudflareResult(w, `{"name":"repo-wrangler","uuid":"`+cloudflareTestDatabaseID+`"}`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/query"):
			var query struct {
				SQL string `json:"sql"`
			}
			if err := json.NewDecoder(r.Body).Decode(&query); err != nil {
				t.Fatal(err)
			}
			switch {
			case strings.Contains(query.SQL, "_ranch_hand_installation") && strings.HasPrefix(query.SQL, "SELECT"):
				deploymentID, _ := lifecycle.DeploymentID(cloudflareEvaluationPlan())
				writeCloudflareResult(w, `[{"success":true,"results":[{"deployment_id":"`+deploymentID+`","release_version":"v1.2.3"}]}]`)
			case strings.Contains(query.SQL, "_ranch_hand_installation"):
				markerWritten = true
				writeCloudflareResult(w, `[{"success":true,"results":[]}]`)
			case strings.Contains(query.SQL, "CREATE TABLE example"):
				migrationApplied = true
				writeCloudflareResult(w, `[{"success":true,"results":[]}]`)
			default:
				t.Fatalf("unexpected D1 query: %s", query.SQL)
			}
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/assets-upload-session"):
			var request struct {
				Manifest map[string]struct {
					Hash string `json:"hash"`
				} `json:"manifest"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Manifest) != 1 {
				t.Fatal("invalid asset manifest")
			}
			assetHash = request.Manifest["/index.html"].Hash
			if len(assetHash) != 32 {
				t.Fatal("asset hash does not follow Cloudflare's direct-upload contract")
			}
			writeCloudflareResult(w, `{"buckets":[["`+assetHash+`"]],"jwt":"upload-jwt"}`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/workers/assets/upload"):
			if r.Header.Get("Authorization") != "Bearer upload-jwt" || !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data;") {
				t.Fatal("asset bucket did not use the upload-session token and multipart protocol")
			}
			if err := r.ParseMultipartForm(2 << 20); err != nil || len(r.MultipartForm.Value[assetHash]) != 1 {
				t.Fatal("asset bucket did not include the requested hash")
			}
			assetsUploaded = true
			writeCloudflareResult(w, `{"jwt":"completion-jwt"}`)
		case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/workers/scripts/repo-wrangler"):
			if err := r.ParseMultipartForm(20 << 20); err != nil {
				t.Fatal(err)
			}
			var metadata struct {
				CompatibilityDate string `json:"compatibility_date"`
				Bindings          []struct {
					Type       string `json:"type"`
					Name       string `json:"name"`
					DatabaseID string `json:"database_id"`
					Text       string `json:"text"`
				} `json:"bindings"`
				Assets struct {
					JWT string `json:"jwt"`
				} `json:"assets"`
			}
			if err := json.Unmarshal([]byte(r.MultipartForm.Value["metadata"][0]), &metadata); err != nil || metadata.Assets.JWT != "completion-jwt" || metadata.CompatibilityDate != "2026-07-01" {
				t.Fatal("Worker metadata did not bind the verified release contract")
			}
			var d1, version bool
			for _, binding := range metadata.Bindings {
				d1 = d1 || (binding.Type == "d1" && binding.DatabaseID == cloudflareTestDatabaseID)
				version = version || (binding.Type == "plain_text" && binding.Name == "APP_VERSION" && binding.Text == "v1.2.3")
			}
			if !d1 || !version || len(r.MultipartForm.File["worker.js"]) != 1 {
				t.Fatal("Worker upload omitted D1, version, or module")
			}
			workerUploaded = true
			writeCloudflareResult(w, `{"id":"repo-wrangler"}`)
		case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/schedules"):
			schedulesUpdated = true
			writeCloudflareResult(w, `{"schedules":[{"cron":"*/5 * * * *"},{"cron":"17 3 * * *"}]}`)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/schedules"):
			writeCloudflareResult(w, `{"schedules":[{"cron":"17 3 * * *"},{"cron":"*/5 * * * *"}]}`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/workers/scripts/repo-wrangler/subdomain"):
			subdomainEnabled = true
			writeCloudflareResult(w, `{"enabled":true,"previews_enabled":false}`)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/workers/subdomain"):
			writeCloudflareResult(w, `{"subdomain":"wranglerlabs"}`)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/workers/scripts/repo-wrangler/subdomain"):
			writeCloudflareResult(w, `{"enabled":true,"previews_enabled":false}`)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/settings"):
			writeCloudflareResult(w, `{"compatibility_date":"2026-07-01","bindings":[{"type":"assets","name":"ASSETS"},{"type":"d1","name":"DB","database_id":"`+cloudflareTestDatabaseID+`"},{"type":"plain_text","name":"ALLOWED_GITHUB_USERS","text":""},{"type":"plain_text","name":"APP_VERSION","text":"v1.2.3"},{"type":"plain_text","name":"AUTH_MODE","text":"github_app"},{"type":"plain_text","name":"DEMO_MODE","text":"true"}]}`)
		default:
			t.Fatalf("unexpected Cloudflare request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()
	adapter := newCloudflare(server.Client(), server.URL)
	adapter.healthClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "repo-wrangler.wranglerlabs.workers.dev" || r.URL.Scheme != "https" {
			t.Fatalf("health check escaped Cloudflare-managed HTTPS: %s", r.URL)
		}
		body := `{"ok":true}`
		if r.URL.Path == "/health/live" {
			body = `{"ok":true,"version":"v1.2.3"}`
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	candidate := cloudflareEvaluationPlan()
	credentials := Credentials{CloudflareAPIToken: "cf-token"}
	if err := adapter.Apply(context.Background(), lifecycle.Install, candidate, "", stagedCloudflareBundle(t), lifecycle.OperationBackups{}, credentials); err != nil {
		t.Fatal(err)
	}
	if !databaseCreated || !markerWritten || !migrationApplied || !assetsUploaded || !workerUploaded || !schedulesUpdated || !subdomainEnabled {
		t.Fatal("Cloudflare evaluation install did not complete every native API phase")
	}
	if err := adapter.Verify(context.Background(), candidate, credentials); err != nil {
		t.Fatal(err)
	}
}

func TestCloudflareRecoveryDeletesOnlyMarkerOwnedResources(t *testing.T) {
	candidate := cloudflareEvaluationPlan()
	deploymentID, _ := lifecycle.DeploymentID(candidate)
	var workerDeleted, databaseDeleted bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/d1/database"):
			writeCloudflareResult(w, `[{"name":"repo-wrangler","uuid":"`+cloudflareTestDatabaseID+`"}]`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/query"):
			writeCloudflareResult(w, `[{"success":true,"results":[{"deployment_id":"`+deploymentID+`","release_version":"v1.2.3"}]}]`)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/workers/scripts/repo-wrangler"):
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "export default {}")
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/settings"):
			writeCloudflareResult(w, `{"compatibility_date":"2026-07-01","bindings":[{"type":"d1","name":"DB","database_id":"`+cloudflareTestDatabaseID+`"},{"type":"plain_text","name":"APP_VERSION","text":"v1.2.3"}]}`)
		case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/workers/scripts/repo-wrangler"):
			workerDeleted = true
			writeCloudflareResult(w, `{}`)
		case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/d1/database/"+cloudflareTestDatabaseID):
			databaseDeleted = true
			writeCloudflareResult(w, `{}`)
		default:
			t.Fatalf("unexpected recovery request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()
	adapter := newCloudflare(server.Client(), server.URL)
	if err := adapter.Recover(context.Background(), lifecycle.Install, candidate, "", lifecycle.OperationBackups{}, Credentials{CloudflareAPIToken: "cf-token"}); err != nil {
		t.Fatal(err)
	}
	if !workerDeleted || !databaseDeleted {
		t.Fatal("owned failed-install Cloudflare resources were not deleted")
	}
}

func TestCloudflareUninstallDeletesOnlyMarkerOwnedResources(t *testing.T) {
	candidate := cloudflareEvaluationPlan()
	deploymentID, _ := lifecycle.DeploymentID(candidate)
	var workerDeleted, databaseDeleted bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/d1/database"):
			writeCloudflareResult(w, `[{"name":"repo-wrangler","uuid":"`+cloudflareTestDatabaseID+`"}]`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/query"):
			writeCloudflareResult(w, `[{"success":true,"results":[{"deployment_id":"`+deploymentID+`","release_version":"v1.2.3"}]}]`)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/workers/scripts/repo-wrangler"):
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/settings"):
			writeCloudflareResult(w, `{"compatibility_date":"2026-07-01","bindings":[{"type":"d1","name":"DB","database_id":"`+cloudflareTestDatabaseID+`"},{"type":"plain_text","name":"APP_VERSION","text":"v1.2.3"}]}`)
		case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/workers/scripts/repo-wrangler"):
			workerDeleted = true
			writeCloudflareResult(w, `{}`)
		case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/d1/database/"+cloudflareTestDatabaseID):
			databaseDeleted = true
			writeCloudflareResult(w, `{}`)
		default:
			t.Fatalf("unexpected uninstall request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()
	adapter := newCloudflare(server.Client(), server.URL)
	if err := adapter.Apply(context.Background(), lifecycle.Uninstall, candidate, candidate.Release.Version, bundle.StagedBundle{}, lifecycle.OperationBackups{}, Credentials{CloudflareAPIToken: "cf-token"}); err != nil {
		t.Fatal(err)
	}
	if !workerDeleted || !databaseDeleted {
		t.Fatal("owned Cloudflare resources were not uninstalled")
	}
}

func TestCloudflareRecoveryRefusesUnownedDatabase(t *testing.T) {
	deleted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet:
			writeCloudflareResult(w, `[{"name":"repo-wrangler","uuid":"`+cloudflareTestDatabaseID+`"}]`)
		case r.Method == http.MethodPost:
			writeCloudflareResult(w, `[{"success":true,"results":[{"deployment_id":"someone-else","release_version":"v1.2.3"}]}]`)
		case r.Method == http.MethodDelete:
			deleted = true
			writeCloudflareResult(w, `{}`)
		}
	}))
	defer server.Close()
	err := newCloudflare(server.Client(), server.URL).Recover(context.Background(), lifecycle.Install, cloudflareEvaluationPlan(), "", lifecycle.OperationBackups{}, Credentials{CloudflareAPIToken: "cf-token"})
	if err == nil || deleted {
		t.Fatal("Cloudflare recovery deleted or accepted an unowned database")
	}
}

func TestCloudflareErrorIncludesBoundedSanitizedAPIMessage(t *testing.T) {
	apiMessage := "schedule rejected\r\n" + strings.Repeat("é", 600)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": false,
			"errors":  []map[string]any{{"code": 10021, "message": apiMessage}},
		})
	}))
	defer server.Close()

	request, err := http.NewRequest(http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = newCloudflare(server.Client(), server.URL).doCloudflare(request, nil)
	if err == nil {
		t.Fatal("expected Cloudflare API error")
	}
	message := err.Error()
	if !strings.HasPrefix(message, "Cloudflare returned HTTP 400 (code 10021): schedule rejected") {
		t.Fatalf("Cloudflare error omitted the API diagnostic: %q", message)
	}
	if strings.ContainsAny(message, "\r\n") {
		t.Fatalf("Cloudflare error retained control characters: %q", message)
	}
	detail := strings.TrimPrefix(message, "Cloudflare returned HTTP 400 (code 10021): ")
	if len([]rune(detail)) != 512 {
		t.Fatalf("Cloudflare error detail was not bounded to 512 characters: %d", len([]rune(detail)))
	}
}
