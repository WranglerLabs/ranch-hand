package server

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/WranglerLabs/ranch-hand/internal/adapter"
	"github.com/WranglerLabs/ranch-hand/internal/bundle"
	"github.com/WranglerLabs/ranch-hand/internal/lifecycle"
	"github.com/WranglerLabs/ranch-hand/internal/operations"
	"github.com/WranglerLabs/ranch-hand/internal/plan"
	productrelease "github.com/WranglerLabs/ranch-hand/internal/release"
)

func testUI() fs.FS { return fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}} }

type fakeReleaseVerifier struct {
	request          productrelease.Request
	cachePath        string
	discoveryChannel string
	discoveryTarget  string
}

type fakeTargetPreflighter struct {
	credentials adapter.Credentials
}

type fakeBundleStager struct {
	artifact productrelease.VerifiedArtifact
}

type fakeOperationRunner struct {
	request              operations.Request
	recoveryDeploymentID string
}

func (f *fakeOperationRunner) RecoverActive(_ context.Context, deploymentID string, _ adapter.Credentials) (operations.Result, error) {
	f.recoveryDeploymentID = deploymentID
	return operations.Result{Journal: lifecycle.Journal{Phase: lifecycle.Recovered}, Recovered: true}, nil
}

type fakeInstallationReader struct {
	records []lifecycle.InstallationRecord
	backups []lifecycle.BackupRecord
	active  []lifecycle.Journal
	err     error
}

type fakeRollbackPool struct {
	entries    []adapter.RollbackPoolEntry
	keepLatest int
}

func (f *fakeRollbackPool) RollbackPool(context.Context, plan.DeploymentPlan, []lifecycle.BackupRecord) ([]adapter.RollbackPoolEntry, error) {
	return f.entries, nil
}

func (f *fakeRollbackPool) PruneRollbackPool(_ context.Context, _ plan.DeploymentPlan, _ []lifecycle.BackupRecord, keepLatest int) (adapter.RollbackPruneResult, error) {
	f.keepLatest = keepLatest
	return adapter.RollbackPruneResult{Kept: f.entries}, nil
}

func (f *fakeInstallationReader) Installation(deploymentID string) (lifecycle.InstallationRecord, error) {
	for _, record := range f.records {
		if record.DeploymentID == deploymentID {
			return record, f.err
		}
	}
	return lifecycle.InstallationRecord{}, os.ErrNotExist
}

func (f *fakeInstallationReader) Installations() ([]lifecycle.InstallationRecord, error) {
	return f.records, f.err
}

func (f *fakeInstallationReader) Backups(string) ([]lifecycle.BackupRecord, error) {
	return f.backups, f.err
}

func (f *fakeInstallationReader) Active(deploymentID string) (lifecycle.Journal, error) {
	for _, journal := range f.active {
		if journal.DeploymentID == deploymentID {
			return journal, f.err
		}
	}
	return lifecycle.Journal{}, os.ErrNotExist
}

func (f *fakeInstallationReader) ActiveOperations() ([]lifecycle.Journal, error) {
	return f.active, f.err
}

func (f *fakeOperationRunner) Run(_ context.Context, request operations.Request) (operations.Result, error) {
	f.request = request
	return operations.Result{Journal: lifecycle.Journal{Phase: lifecycle.Committed}}, nil
}

func (f *fakeBundleStager) Stage(artifact productrelease.VerifiedArtifact) (bundle.StagedBundle, error) {
	f.artifact = artifact
	return bundle.StagedBundle{Product: productrelease.Product, Version: artifact.Version, Target: artifact.Target, Path: `C:\staged\compose`}, nil
}

func (f *fakeTargetPreflighter) Preflight(_ context.Context, candidate plan.DeploymentPlan, credentials adapter.Credentials) adapter.Report {
	f.credentials = credentials
	return adapter.Report{Ready: true, Target: candidate.Target.Kind, Checks: []adapter.Check{{Name: "native-api", OK: true, Message: "connected"}}}
}

func (f *fakeReleaseVerifier) VerifyAndCache(_ context.Context, request productrelease.Request) (productrelease.VerifiedArtifact, error) {
	f.request = request
	return productrelease.VerifiedArtifact{
		Product: "RepoWrangler", Version: request.Version, Target: request.Target, CachePath: f.cachePath,
		ManifestURL: request.ManifestURL, ManifestSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		SHA256: "1e6ed65d77d6364eeaed5a745ba5c4985ae2b700dd85d7cf7f027bdf294a33fc", Size: 6,
		ProvenanceVerified: true, SBOMVerified: true,
	}, nil
}

func (f *fakeReleaseVerifier) Discover(_ context.Context, channel, target string) (productrelease.DiscoveredRelease, error) {
	f.discoveryChannel = channel
	f.discoveryTarget = target
	return productrelease.DiscoveredRelease{
		Version: "v1.0.10", ManifestURL: "https://github.com/WranglerLabs/repo-wrangler/releases/download/v1.0.10/release-manifest.json",
	}, nil
}

func TestCreateExportPreflightAndDryRunVerifiedPlan(t *testing.T) {
	artifact := t.TempDir() + string(os.PathSeparator) + "bundle.tar.gz"
	if err := os.WriteFile(artifact, []byte("bundle"), 0o600); err != nil {
		t.Fatal(err)
	}
	verifier := &fakeReleaseVerifier{cachePath: artifact}
	targets := &fakeTargetPreflighter{}
	stager := &fakeBundleStager{}
	runner := &fakeOperationRunner{}
	h := newWithServices("secret-token", "test", testUI(), verifier, targets, stager, runner, nil, nil)
	manifest := "https://github.com/WranglerLabs/repo-wrangler/releases/download/v1.2.3/release-manifest.json"
	verifyBody := `{"manifestUrl":"` + manifest + `","version":"v1.2.3","target":"local-compose"}`
	response := authorizedPost(h, "/api/v1/releases/verify", verifyBody)
	if response.Code != http.StatusOK {
		t.Fatalf("verification returned %d: %s", response.Code, response.Body.String())
	}

	createBody := `{"name":"Local Wrangler","version":"v1.2.3","target":"local-compose","configuration":{"projectName":"repo-wrangler","dataVolume":"repo-wrangler-data","listenAddress":"127.0.0.1:8080"}}`
	response = authorizedPost(h, "/api/v1/plans/create", createBody)
	if response.Code != http.StatusCreated {
		t.Fatalf("plan creation returned %d: %s", response.Code, response.Body.String())
	}
	var created struct {
		Plan          json.RawMessage `json:"plan"`
		CanonicalJSON string          `json:"canonicalJson"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(created.CanonicalJSON, "\n") {
		t.Fatal("canonical plan must end with a newline")
	}

	response = authorizedPost(h, "/api/v1/plans/export", string(created.Plan))
	if response.Code != http.StatusOK || !strings.Contains(response.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("plan export returned %d: %s", response.Code, response.Body.String())
	}
	response = authorizedPost(h, "/api/v1/plans/dry-run", string(created.Plan))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"mutated":false`) {
		t.Fatalf("dry run returned %d: %s", response.Code, response.Body.String())
	}
	response = authorizedPost(h, "/api/v1/plans/preflight", string(created.Plan))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"ready":true`) {
		t.Fatalf("preflight returned %d: %s", response.Code, response.Body.String())
	}
	operationBody := `{"kind":"install","plan":` + string(created.Plan) + `,"credentials":{}}`
	response = authorizedPost(h, "/api/v1/operations/run", operationBody)
	if response.Code != http.StatusConflict {
		t.Fatalf("operation without live target preflight returned %d: %s", response.Code, response.Body.String())
	}
	targetBody := `{"plan":` + string(created.Plan) + `,"credentials":{"sshPassword":"runtime-only"}}`
	response = authorizedPost(h, "/api/v1/targets/preflight", targetBody)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"ready":true`) {
		t.Fatalf("target preflight returned %d: %s", response.Code, response.Body.String())
	}
	if targets.credentials.SSHPassword != "runtime-only" {
		t.Fatal("target adapter did not receive runtime credential")
	}
	response = authorizedPost(h, "/api/v1/bundles/stage", string(created.Plan))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"staged":true`) {
		t.Fatalf("bundle staging returned %d: %s", response.Code, response.Body.String())
	}
	if stager.artifact.SHA256 == "" || stager.artifact.Target != "local-compose" {
		t.Fatal("stager did not receive the verified artifact")
	}
	response = authorizedPost(h, "/api/v1/operations/run", operationBody)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"completed":true`) {
		t.Fatalf("operation returned %d: %s", response.Code, response.Body.String())
	}
	if runner.request.Kind != lifecycle.Install || runner.request.Artifact.SHA256 == "" {
		t.Fatal("coordinator did not receive verified local install request")
	}
	backupBody := `{"kind":"backup","fromVersion":"v1.2.3","plan":` + string(created.Plan) + `,"credentials":{}}`
	response = authorizedPost(h, "/api/v1/operations/run", backupBody)
	if response.Code != http.StatusOK || runner.request.Kind != lifecycle.Backup || runner.request.FromVersion != "v1.2.3" {
		t.Fatalf("backup operation was not routed to the coordinator: %d %s", response.Code, response.Body.String())
	}
	updateBody := `{"kind":"update","fromVersion":"v1.2.2","plan":` + string(created.Plan) + `,"credentials":{}}`
	response = authorizedPost(h, "/api/v1/operations/run", updateBody)
	if response.Code != http.StatusOK || runner.request.Kind != lifecycle.Update || runner.request.FromVersion != "v1.2.2" {
		t.Fatalf("update operation was not routed to the coordinator: %d %s", response.Code, response.Body.String())
	}
	restoreID := strings.Repeat("c", 32)
	restoreBody := `{"kind":"restore","fromVersion":"v1.2.3","backupId":"` + restoreID + `","plan":` + string(created.Plan) + `,"credentials":{}}`
	response = authorizedPost(h, "/api/v1/operations/run", restoreBody)
	if response.Code != http.StatusOK || runner.request.Kind != lifecycle.Restore || runner.request.BackupID != restoreID {
		t.Fatalf("restore operation was not routed to the coordinator: %d %s", response.Code, response.Body.String())
	}
	repairBody := `{"kind":"repair","fromVersion":"v1.2.3","plan":` + string(created.Plan) + `,"credentials":{}}`
	response = authorizedPost(h, "/api/v1/operations/run", repairBody)
	if response.Code != http.StatusOK || runner.request.Kind != lifecycle.Repair {
		t.Fatalf("repair operation was not routed to the coordinator: %d %s", response.Code, response.Body.String())
	}
	deploymentID := "0123456789abcdef01234567"
	response = authorizedPost(h, "/api/v1/operations/"+deploymentID+"/recover", `{"credentials":{}}`)
	if response.Code != http.StatusOK || runner.recoveryDeploymentID != deploymentID || !strings.Contains(response.Body.String(), `"recovered":true`) {
		t.Fatalf("active recovery was not routed to the coordinator: %d %s", response.Code, response.Body.String())
	}
}

func authorizedPost(h http.Handler, path, body string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer secret-token")
	request.Header.Set("Origin", "http://example.com")
	request.Host = "example.com"
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	return response
}

func TestRecommendsLatestCompatibleRelease(t *testing.T) {
	verifier := &fakeReleaseVerifier{}
	h := NewWithReleaseVerifier("secret-token", "test", testUI(), verifier)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/releases/recommended?channel=stable&target=local-compose", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"version":"v1.0.10"`) {
		t.Fatalf("recommended release returned %d: %s", response.Code, response.Body.String())
	}
	if verifier.discoveryChannel != "stable" || verifier.discoveryTarget != "local-compose" {
		t.Fatalf("unexpected discovery request: %s / %s", verifier.discoveryChannel, verifier.discoveryTarget)
	}
}

func TestListsInstallationRecords(t *testing.T) {
	reader := &fakeInstallationReader{records: []lifecycle.InstallationRecord{{
		DeploymentID: "0123456789abcdef01234567", Target: "local-compose",
		State: lifecycle.InstallationActive, Version: "v1.2.3",
	}}, backups: []lifecycle.BackupRecord{{BackupID: strings.Repeat("b", 32), Version: "v1.2.2"}}, active: []lifecycle.Journal{{
		DeploymentID: "0123456789abcdef01234567", OperationID: strings.Repeat("d", 32), Kind: lifecycle.Update,
		Target: "local-compose", FromVersion: "v1.2.2", ToVersion: "v1.2.3", Phase: lifecycle.Staged,
	}}}
	h := newWithServices("secret-token", "test", testUI(), nil, nil, nil, nil, reader, nil)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/installations", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"version":"v1.2.3"`) || !strings.Contains(response.Body.String(), `"state":"active"`) {
		t.Fatalf("installation inventory returned %d: %s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodGet, "/api/v1/installations/0123456789abcdef01234567/backups", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	response = httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"version":"v1.2.2"`) {
		t.Fatalf("backup inventory returned %d: %s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodGet, "/api/v1/diagnostics", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	response = httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Header().Get("Content-Disposition"), "attachment") ||
		!strings.Contains(response.Body.String(), `"name":"Ranch Hand"`) || strings.Contains(response.Body.String(), `"plan"`) || strings.Contains(response.Body.String(), `"locator"`) {
		t.Fatalf("diagnostics export returned %d: %s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodGet, "/api/v1/operations/active", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	response = httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"phase":"staged"`) {
		t.Fatalf("active operation inventory returned %d: %s", response.Code, response.Body.String())
	}
}

func TestListsAndExplicitlyPrunesLocalRollbackPool(t *testing.T) {
	candidate := plan.DeploymentPlan{
		SchemaVersion: plan.CurrentSchemaVersion, Name: "Local Wrangler",
		Release: plan.ReleaseSelection{Version: "v1.2.3", ManifestURL: "https://github.com/WranglerLabs/repo-wrangler/releases/download/v1.2.3/release-manifest.json", ManifestSHA256: strings.Repeat("a", 64), ArtifactSHA256: strings.Repeat("b", 64), ArtifactSize: 42},
		Target:  plan.Target{Kind: "local-compose"}, Configuration: map[string]string{"projectName": "repo-wrangler", "dataVolume": "repo-wrangler-data", "listenAddress": "127.0.0.1:8080"},
	}
	encoded, err := plan.CanonicalJSON(candidate)
	if err != nil {
		t.Fatal(err)
	}
	deploymentID, err := lifecycle.DeploymentID(candidate)
	if err != nil {
		t.Fatal(err)
	}
	reader := &fakeInstallationReader{records: []lifecycle.InstallationRecord{{DeploymentID: deploymentID, Target: "local-compose", State: lifecycle.InstallationActive, Version: "v1.2.3", Plan: encoded}}}
	pool := &fakeRollbackPool{entries: []adapter.RollbackPoolEntry{{BackupID: strings.Repeat("c", 32), Version: "v1.2.2"}}}
	h := newWithServices("secret-token", "test", testUI(), nil, nil, nil, nil, reader, pool)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/installations/"+deploymentID+"/rollback-pool", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"version":"v1.2.2"`) {
		t.Fatalf("rollback pool inventory returned %d: %s", response.Code, response.Body.String())
	}
	response = authorizedPost(h, "/api/v1/installations/"+deploymentID+"/rollback-pool/prune", `{"keepLatest":1,"confirmed":true}`)
	if response.Code != http.StatusOK || pool.keepLatest != 1 {
		t.Fatalf("rollback pool prune returned %d: %s", response.Code, response.Body.String())
	}
	response = authorizedPost(h, "/api/v1/installations/"+deploymentID+"/rollback-pool/prune", `{"keepLatest":0,"confirmed":false}`)
	if response.Code != http.StatusBadRequest || pool.keepLatest != 1 {
		t.Fatalf("unconfirmed rollback prune was accepted: %d %s", response.Code, response.Body.String())
	}
}

func TestTargetPreflightRecognizesInterruptedAndExistingLifecycleState(t *testing.T) {
	candidate := plan.DeploymentPlan{
		SchemaVersion: plan.CurrentSchemaVersion, Name: "Local WSL Wrangler",
		Release: plan.ReleaseSelection{Version: "v1.2.3", ManifestURL: "https://github.com/WranglerLabs/repo-wrangler/releases/download/v1.2.3/release-manifest.json", ManifestSHA256: strings.Repeat("a", 64), ArtifactSHA256: strings.Repeat("b", 64), ArtifactSize: 42},
		Target:  plan.Target{Kind: "local-wsl-compose"}, Configuration: map[string]string{"distribution": "Ubuntu", "projectName": "repo-wrangler-ranch-hand"},
	}
	deploymentID, err := lifecycle.DeploymentID(candidate)
	if err != nil {
		t.Fatal(err)
	}
	blocked := adapter.Report{Target: candidate.Target.Kind, Checks: []adapter.Check{{Name: "wsl-compose-boundary", Message: "directory exists"}}}

	interrupted := &fakeInstallationReader{active: []lifecycle.Journal{{DeploymentID: deploymentID, Kind: lifecycle.Install, Phase: lifecycle.Staged}}}
	server := &Server{installations: interrupted}
	report := server.annotateLifecycleTarget(candidate, blocked)
	if report.State != "recovery-required" || report.DeploymentID != deploymentID || report.Ready || !strings.Contains(report.Checks[0].Message, "ownership-checked recovery") {
		t.Fatalf("interrupted operation was not surfaced safely: %#v", report)
	}

	existing := &fakeInstallationReader{records: []lifecycle.InstallationRecord{{DeploymentID: deploymentID, State: lifecycle.InstallationActive, Version: "v1.2.3"}}}
	server.installations = existing
	report = server.annotateLifecycleTarget(candidate, blocked)
	if report.State != "already-installed" || report.DeploymentID != deploymentID || report.Ready || !strings.Contains(report.Checks[0].Message, "active installation record") {
		t.Fatalf("existing installation was not surfaced safely: %#v", report)
	}
}

func TestStatusRequiresLaunchToken(t *testing.T) {
	h := New("secret-token", "test", testUI())
	request := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("got %d", response.Code)
	}

	request = httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
	request.Header.Set("Authorization", "Bearer secret-token")
	response = httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("got %d: %s", response.Code, response.Body.String())
	}
}

func TestMutationRejectsCrossOrigin(t *testing.T) {
	h := New("secret-token", "test", testUI())
	request := httptest.NewRequest(http.MethodPost, "/api/v1/plans/validate", strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer secret-token")
	request.Header.Set("Origin", "https://attacker.example")
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("got %d", response.Code)
	}
}

func TestSecurityHeaders(t *testing.T) {
	h := New("secret-token", "test", testUI())
	response := httptest.NewRecorder()
	h.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
	if response.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("missing CSP")
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("missing no-store")
	}
}

func TestVerifyReleaseRequiresTokenAndSameOrigin(t *testing.T) {
	verifier := &fakeReleaseVerifier{}
	h := NewWithReleaseVerifier("secret-token", "test", testUI(), verifier)
	body := `{"manifestUrl":"https://github.com/WranglerLabs/repo-wrangler/releases/download/v1.2.3/release-manifest.json","version":"v1.2.3","target":"local-compose"}`

	request := httptest.NewRequest(http.MethodPost, "/api/v1/releases/verify", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer secret-token")
	request.Header.Set("Origin", "https://attacker.example")
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("cross-origin request returned %d", response.Code)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/releases/verify", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer secret-token")
	request.Header.Set("Origin", "http://example.com")
	request.Host = "example.com"
	response = httptest.NewRecorder()
	h.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("verification returned %d: %s", response.Code, response.Body.String())
	}
	if verifier.request.Version != "v1.2.3" || verifier.request.Target != "local-compose" {
		t.Fatalf("unexpected verifier request: %+v", verifier.request)
	}
}
