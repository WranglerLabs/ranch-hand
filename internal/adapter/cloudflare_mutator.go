package adapter

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/WranglerLabs/ranch-hand/internal/bundle"
	"github.com/WranglerLabs/ranch-hand/internal/lifecycle"
	"github.com/WranglerLabs/ranch-hand/internal/plan"
)

const (
	maximumCloudflareAsset          = int64(25 << 20)
	maximumCloudflareAssets         = int64(100 << 20)
	maximumCloudflareWorker         = int64(16 << 20)
	maximumCloudflareMigration      = int64(4 << 20)
	maximumCloudflareMigrations     = int64(32 << 20)
	maximumCloudflareAssetCount     = 20_000
	maximumCloudflareMigrationCount = 1_000
)

var cloudflareManagedHostname = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.workers\.dev$`)

type cloudflareEnvelope struct {
	Success bool            `json:"success"`
	Result  json.RawMessage `json:"result"`
}

type cloudflareDatabase struct {
	Name string `json:"name"`
	UUID string `json:"uuid"`
}

type cloudflareAsset struct {
	Path        string
	Hash        string
	ContentType string
	Contents    []byte
}

type cloudflareExpected struct {
	DatabaseID string
	Identity   bundle.Identity
}

func (c *Cloudflare) Backup(context.Context, plan.DeploymentPlan, Credentials) (lifecycle.BackupArtifact, error) {
	return lifecycle.BackupArtifact{}, errors.New("Cloudflare D1 backup is not implemented")
}

func (c *Cloudflare) Apply(ctx context.Context, kind lifecycle.OperationKind, candidate plan.DeploymentPlan, staged bundle.StagedBundle, backup *lifecycle.BackupRecord, credentials Credentials) error {
	if kind != lifecycle.Install || backup != nil {
		return errors.New("the Cloudflare adapter currently supports only a new evaluation install")
	}
	if err := candidate.Validate(); err != nil {
		return err
	}
	if err := credentials.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(credentials.CloudflareAPIToken) == "" {
		return errors.New("an in-memory Cloudflare API token is required")
	}
	if candidate.Configuration["customDomain"] != "" {
		return errors.New("Cloudflare custom-domain binding is not enabled in this evaluation adapter")
	}
	identity, err := bundle.ReadIdentity(staged)
	if err != nil {
		return err
	}
	if staged.Target != "cloudflare" {
		return errors.New("Cloudflare adapter requires a cloudflare bundle")
	}

	account, worker, database := cloudflareNames(candidate)
	headers := cloudflareHeaders(credentials)
	if err := c.requireAvailable(ctx, account, worker, database, headers); err != nil {
		return err
	}
	deploymentID, err := lifecycle.DeploymentID(candidate)
	if err != nil {
		return err
	}

	created, err := c.createDatabase(ctx, account, database, headers)
	if err != nil {
		return fmt.Errorf("create dedicated Cloudflare D1 database: %w", err)
	}
	if err := c.writeOwnershipMarker(ctx, account, created.UUID, deploymentID, candidate.Release.Version, headers); err != nil {
		return fmt.Errorf("record D1 ownership marker: %w", err)
	}
	if err := c.applyMigrations(ctx, account, created.UUID, staged, identity, headers); err != nil {
		return fmt.Errorf("apply verified D1 migrations: %w", err)
	}
	completionToken, err := c.uploadAssets(ctx, account, worker, staged, identity, headers)
	if err != nil {
		return fmt.Errorf("upload verified Worker assets: %w", err)
	}
	if err := c.uploadWorker(ctx, account, worker, created.UUID, deploymentID, completionToken, staged, identity, headers); err != nil {
		return fmt.Errorf("upload verified Worker module: %w", err)
	}
	if err := c.updateSchedules(ctx, account, worker, identity.Crons, headers); err != nil {
		return fmt.Errorf("configure verified Worker schedules: %w", err)
	}
	if err := c.enableWorkersDev(ctx, account, worker, headers); err != nil {
		return fmt.Errorf("enable Cloudflare-managed HTTPS endpoint: %w", err)
	}
	c.rememberExpected(deploymentID, created.UUID, identity)
	return nil
}

func (c *Cloudflare) rememberExpected(deploymentID, databaseID string, identity bundle.Identity) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expected[deploymentID] = cloudflareExpected{DatabaseID: databaseID, Identity: identity}
}

func (c *Cloudflare) expectedDeployment(deploymentID string) (cloudflareExpected, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	expected, ok := c.expected[deploymentID]
	return expected, ok
}

func cloudflareNames(candidate plan.DeploymentPlan) (string, string, string) {
	return url.PathEscape(candidate.Configuration["accountId"]), url.PathEscape(candidate.Configuration["workerName"]), candidate.Configuration["databaseName"]
}

func cloudflareHeaders(credentials Credentials) map[string]string {
	return map[string]string{"Authorization": "Bearer " + credentials.CloudflareAPIToken}
}

func (c *Cloudflare) requireAvailable(ctx context.Context, account, worker, database string, headers map[string]string) error {
	status, err := controlPlaneJSON(ctx, c.client, http.MethodGet, c.baseURL+"/accounts/"+account+"/workers/scripts/"+worker, headers, nil)
	if status != http.StatusNotFound {
		if err == nil {
			return errors.New("refusing to replace a pre-existing Cloudflare Worker")
		}
		return fmt.Errorf("verify Worker availability: %w", err)
	}
	databases, err := c.findDatabases(ctx, account, database, headers)
	if err != nil {
		return fmt.Errorf("verify D1 database availability: %w", err)
	}
	if len(databases) != 0 {
		return errors.New("refusing to replace a pre-existing Cloudflare D1 database")
	}
	return nil
}

func (c *Cloudflare) findDatabases(ctx context.Context, account, name string, headers map[string]string) ([]cloudflareDatabase, error) {
	destination := c.baseURL + "/accounts/" + account + "/d1/database?name=" + url.QueryEscape(name) + "&per_page=100"
	var result []cloudflareDatabase
	if err := c.cloudflareJSON(ctx, http.MethodGet, destination, headers, nil, &result); err != nil {
		return nil, err
	}
	filtered := result[:0]
	for _, item := range result {
		if item.Name == name && item.UUID != "" {
			filtered = append(filtered, item)
		}
	}
	return filtered, nil
}

func (c *Cloudflare) createDatabase(ctx context.Context, account, name string, headers map[string]string) (cloudflareDatabase, error) {
	var created cloudflareDatabase
	err := c.cloudflareJSON(ctx, http.MethodPost, c.baseURL+"/accounts/"+account+"/d1/database", headers, map[string]string{"name": name}, &created)
	if err != nil {
		return cloudflareDatabase{}, err
	}
	if created.Name != name || created.UUID == "" {
		return cloudflareDatabase{}, errors.New("Cloudflare returned an invalid D1 identity")
	}
	return created, nil
}

func (c *Cloudflare) writeOwnershipMarker(ctx context.Context, account, databaseID, deploymentID, version string, headers map[string]string) error {
	create := `CREATE TABLE IF NOT EXISTS _ranch_hand_installation (singleton INTEGER PRIMARY KEY CHECK (singleton = 1), deployment_id TEXT NOT NULL, release_version TEXT NOT NULL)`
	if err := c.queryD1(ctx, account, databaseID, create, nil, headers); err != nil {
		return err
	}
	insert := `INSERT OR REPLACE INTO _ranch_hand_installation (singleton, deployment_id, release_version) VALUES (1, ?1, ?2)`
	return c.queryD1(ctx, account, databaseID, insert, []string{deploymentID, version}, headers)
}

func (c *Cloudflare) queryD1(ctx context.Context, account, databaseID, sql string, params []string, headers map[string]string) error {
	input := struct {
		SQL    string   `json:"sql"`
		Params []string `json:"params,omitempty"`
	}{SQL: sql, Params: params}
	var results []struct {
		Success bool              `json:"success"`
		Results []json.RawMessage `json:"results"`
	}
	destination := c.baseURL + "/accounts/" + account + "/d1/database/" + url.PathEscape(databaseID) + "/query"
	if err := c.cloudflareJSON(ctx, http.MethodPost, destination, headers, input, &results); err != nil {
		return err
	}
	if len(results) == 0 {
		return errors.New("Cloudflare D1 returned no query result")
	}
	for _, result := range results {
		if !result.Success {
			return errors.New("Cloudflare D1 query did not succeed")
		}
	}
	return nil
}

func (c *Cloudflare) applyMigrations(ctx context.Context, account, databaseID string, staged bundle.StagedBundle, identity bundle.Identity, headers map[string]string) error {
	directory := filepath.Join(staged.Path, identity.MigrationsDirectory)
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	if len(entries) == 0 || len(entries) > maximumCloudflareMigrationCount {
		return errors.New("Cloudflare migration set is empty or exceeds the safety limit")
	}
	var total int64
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".sql") {
			return fmt.Errorf("unexpected Cloudflare migration entry %q", entry.Name())
		}
		details, err := entry.Info()
		if err != nil || !details.Mode().IsRegular() || details.Size() < 1 || details.Size() > maximumCloudflareMigration {
			return fmt.Errorf("migration %q is not a bounded regular file", entry.Name())
		}
		total += details.Size()
		if total > maximumCloudflareMigrations {
			return errors.New("Cloudflare migrations exceed the aggregate safety limit")
		}
		contents, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			return err
		}
		if err := c.queryD1(ctx, account, databaseID, string(contents), nil, headers); err != nil {
			return fmt.Errorf("migration %s: %w", entry.Name(), err)
		}
	}
	return nil
}

func (c *Cloudflare) uploadAssets(ctx context.Context, account, worker string, staged bundle.StagedBundle, identity bundle.Identity, headers map[string]string) (string, error) {
	assets, manifest, err := readCloudflareAssets(filepath.Join(staged.Path, identity.AssetsDirectory))
	if err != nil {
		return "", err
	}
	var session struct {
		Buckets [][]string `json:"buckets"`
		JWT     string     `json:"jwt"`
	}
	destination := c.baseURL + "/accounts/" + account + "/workers/scripts/" + worker + "/assets-upload-session"
	if err := c.cloudflareJSON(ctx, http.MethodPost, destination, headers, map[string]any{"manifest": manifest}, &session); err != nil {
		return "", err
	}
	if session.JWT == "" {
		return "", errors.New("Cloudflare returned no asset upload token")
	}
	completion := session.JWT
	byHash := make(map[string]cloudflareAsset, len(assets))
	for _, asset := range assets {
		byHash[asset.Hash] = asset
	}
	for _, bucket := range session.Buckets {
		var upload struct {
			JWT string `json:"jwt"`
		}
		if err := c.uploadAssetBucket(ctx, account, session.JWT, bucket, byHash, &upload); err != nil {
			return "", err
		}
		if upload.JWT != "" {
			completion = upload.JWT
		}
	}
	if completion == "" {
		return "", errors.New("Cloudflare returned no asset completion token")
	}
	return completion, nil
}

func readCloudflareAssets(directory string) ([]cloudflareAsset, map[string]map[string]any, error) {
	var assets []cloudflareAsset
	manifest := make(map[string]map[string]any)
	var total int64
	err := filepath.WalkDir(directory, func(filename string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if len(assets) >= maximumCloudflareAssetCount {
			return errors.New("Cloudflare asset count exceeds the safety limit")
		}
		details, err := entry.Info()
		if err != nil || !details.Mode().IsRegular() || details.Size() > maximumCloudflareAsset {
			return fmt.Errorf("asset %q is not a bounded regular file", entry.Name())
		}
		total += details.Size()
		if total > maximumCloudflareAssets {
			return errors.New("Cloudflare assets exceed the aggregate safety limit")
		}
		contents, err := os.ReadFile(filename)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(directory, filename)
		if err != nil {
			return err
		}
		extension := strings.TrimPrefix(strings.ToLower(filepath.Ext(relative)), ".")
		hash := sha256.New()
		encoder := base64.NewEncoder(base64.StdEncoding, hash)
		_, _ = encoder.Write(contents)
		_ = encoder.Close()
		_, _ = io.WriteString(hash, extension)
		assetHash := hex.EncodeToString(hash.Sum(nil))[:32]
		assetPath := "/" + filepath.ToSlash(relative)
		contentType := mime.TypeByExtension(filepath.Ext(relative))
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		assets = append(assets, cloudflareAsset{Path: assetPath, Hash: assetHash, ContentType: contentType, Contents: contents})
		manifest[assetPath] = map[string]any{"hash": assetHash, "size": len(contents)}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	if len(assets) == 0 {
		return nil, nil, errors.New("Cloudflare asset set is empty")
	}
	sort.Slice(assets, func(i, j int) bool { return assets[i].Path < assets[j].Path })
	return assets, manifest, nil
}

func (c *Cloudflare) uploadAssetBucket(ctx context.Context, account, uploadToken string, bucket []string, assets map[string]cloudflareAsset, output any) error {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for _, hash := range bucket {
		asset, ok := assets[hash]
		if !ok {
			return errors.New("Cloudflare requested an asset hash outside the verified manifest")
		}
		header := make(textproto.MIMEHeader)
		header.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q`, hash))
		header.Set("Content-Type", asset.ContentType)
		part, err := writer.CreatePart(header)
		if err != nil {
			return err
		}
		encoder := base64.NewEncoder(base64.StdEncoding, part)
		if _, err := encoder.Write(asset.Contents); err != nil {
			return err
		}
		if err := encoder.Close(); err != nil {
			return err
		}
	}
	if err := writer.Close(); err != nil {
		return err
	}
	destination := c.baseURL + "/accounts/" + account + "/workers/assets/upload?base64=true"
	return c.cloudflareMultipart(ctx, http.MethodPost, destination, uploadToken, writer.FormDataContentType(), &body, output)
}

func (c *Cloudflare) uploadWorker(ctx context.Context, account, worker, databaseID, deploymentID, completionToken string, staged bundle.StagedBundle, identity bundle.Identity, headers map[string]string) error {
	workerContents, err := readBoundedFile(filepath.Join(staged.Path, identity.Worker), maximumCloudflareWorker)
	if err != nil {
		return err
	}
	bindings := []map[string]any{
		{"name": identity.AssetsBinding, "type": "assets"},
		{"database_id": databaseID, "name": identity.D1Binding, "type": "d1"},
	}
	keys := make([]string, 0, len(identity.Vars))
	for key := range identity.Vars {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		bindings = append(bindings, map[string]any{"name": key, "text": identity.Vars[key], "type": "plain_text"})
	}
	metadata := map[string]any{
		"main_module":        identity.Worker,
		"compatibility_date": identity.CompatibilityDate,
		"bindings":           bindings,
		"assets": map[string]any{"jwt": completionToken, "config": map[string]any{
			"not_found_handling": identity.AssetsNotFoundHandling, "run_worker_first": identity.AssetsRunWorkerFirst,
		}},
		"observability": map[string]bool{"enabled": identity.ObservabilityEnabled},
		"annotations":   map[string]string{"workers/message": "Installed by Ranch Hand", "workers/tag": deploymentID},
	}
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	metadataHeader := make(textproto.MIMEHeader)
	metadataHeader.Set("Content-Disposition", `form-data; name="metadata"`)
	metadataHeader.Set("Content-Type", "application/json")
	part, err := writer.CreatePart(metadataHeader)
	if err != nil {
		return err
	}
	_, _ = part.Write(metadataJSON)
	moduleHeader := make(textproto.MIMEHeader)
	moduleHeader.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, identity.Worker, identity.Worker))
	moduleHeader.Set("Content-Type", "application/javascript+module")
	part, err = writer.CreatePart(moduleHeader)
	if err != nil {
		return err
	}
	_, _ = part.Write(workerContents)
	if err := writer.Close(); err != nil {
		return err
	}
	destination := c.baseURL + "/accounts/" + account + "/workers/scripts/" + worker
	return c.cloudflareMultipart(ctx, http.MethodPut, destination, strings.TrimPrefix(headers["Authorization"], "Bearer "), writer.FormDataContentType(), &body, nil)
}

func readBoundedFile(filename string, maximum int64) ([]byte, error) {
	details, err := os.Stat(filename)
	if err != nil || !details.Mode().IsRegular() || details.Size() < 1 || details.Size() > maximum {
		return nil, errors.New("verified Cloudflare file is not a bounded regular file")
	}
	return os.ReadFile(filename)
}

func (c *Cloudflare) updateSchedules(ctx context.Context, account, worker string, crons []string, headers map[string]string) error {
	schedules := make([]map[string]string, len(crons))
	for index, cron := range crons {
		schedules[index] = map[string]string{"cron": cron}
	}
	return c.cloudflareJSON(ctx, http.MethodPut, c.baseURL+"/accounts/"+account+"/workers/scripts/"+worker+"/schedules", headers, schedules, nil)
}

func (c *Cloudflare) enableWorkersDev(ctx context.Context, account, worker string, headers map[string]string) error {
	return c.cloudflareJSON(ctx, http.MethodPost, c.baseURL+"/accounts/"+account+"/workers/scripts/"+worker+"/subdomain", headers, map[string]bool{"enabled": true, "previews_enabled": false}, nil)
}

func (c *Cloudflare) Verify(ctx context.Context, candidate plan.DeploymentPlan, credentials Credentials) error {
	if strings.TrimSpace(credentials.CloudflareAPIToken) == "" {
		return errors.New("an in-memory Cloudflare API token is required for verification")
	}
	account, worker, databaseName := cloudflareNames(candidate)
	headers := cloudflareHeaders(credentials)
	databases, err := c.findDatabases(ctx, account, databaseName, headers)
	if err != nil || len(databases) != 1 {
		return errors.New("the dedicated Cloudflare D1 database could not be uniquely verified")
	}
	deploymentID, err := lifecycle.DeploymentID(candidate)
	if err != nil {
		return err
	}
	expected, ok := c.expectedDeployment(deploymentID)
	if !ok || expected.DatabaseID != databases[0].UUID {
		return errors.New("the Cloudflare activation contract is not bound to this Ranch Hand operation")
	}
	if err := c.verifyOwnershipMarker(ctx, account, databases[0].UUID, deploymentID, candidate.Release.Version, headers); err != nil {
		return err
	}
	if err := c.verifyWorkerSettings(ctx, account, worker, databases[0].UUID, candidate.Release.Version, &expected.Identity, headers); err != nil {
		return err
	}
	if err := c.verifySchedules(ctx, account, worker, expected.Identity.Crons, headers); err != nil {
		return err
	}
	var subdomain struct {
		Subdomain string `json:"subdomain"`
	}
	if err := c.cloudflareJSON(ctx, http.MethodGet, c.baseURL+"/accounts/"+account+"/workers/subdomain", headers, nil, &subdomain); err != nil || subdomain.Subdomain == "" {
		return errors.New("Cloudflare account has no managed workers.dev subdomain")
	}
	var workerSubdomain struct {
		Enabled bool `json:"enabled"`
	}
	if err := c.cloudflareJSON(ctx, http.MethodGet, c.baseURL+"/accounts/"+account+"/workers/scripts/"+worker+"/subdomain", headers, nil, &workerSubdomain); err != nil || !workerSubdomain.Enabled {
		return errors.New("Worker is not enabled on Cloudflare-managed HTTPS")
	}
	hostname := candidate.Configuration["workerName"] + "." + subdomain.Subdomain + ".workers.dev"
	if !cloudflareManagedHostname.MatchString(hostname) {
		return errors.New("Cloudflare returned an invalid managed workers.dev hostname")
	}
	return c.verifyManagedHTTPS(ctx, hostname, candidate.Release.Version)
}

func (c *Cloudflare) verifyOwnershipMarker(ctx context.Context, account, databaseID, deploymentID, version string, headers map[string]string) error {
	input := struct {
		SQL string `json:"sql"`
	}{SQL: `SELECT deployment_id, release_version FROM _ranch_hand_installation WHERE singleton = 1`}
	var results []struct {
		Success bool `json:"success"`
		Results []struct {
			DeploymentID string `json:"deployment_id"`
			Version      string `json:"release_version"`
		} `json:"results"`
	}
	destination := c.baseURL + "/accounts/" + account + "/d1/database/" + url.PathEscape(databaseID) + "/query"
	if err := c.cloudflareJSON(ctx, http.MethodPost, destination, headers, input, &results); err != nil || len(results) != 1 || !results[0].Success || len(results[0].Results) != 1 || results[0].Results[0].DeploymentID != deploymentID || results[0].Results[0].Version != version {
		return errors.New("Cloudflare D1 ownership marker does not match this Ranch Hand deployment")
	}
	return nil
}

func (c *Cloudflare) verifyWorkerSettings(ctx context.Context, account, worker, databaseID, version string, expected *bundle.Identity, headers map[string]string) error {
	var settings struct {
		Bindings []struct {
			Type       string `json:"type"`
			Name       string `json:"name"`
			DatabaseID string `json:"database_id"`
			Text       string `json:"text"`
		} `json:"bindings"`
		CompatibilityDate string `json:"compatibility_date"`
	}
	destination := c.baseURL + "/accounts/" + account + "/workers/scripts/" + worker + "/settings"
	if err := c.cloudflareJSON(ctx, http.MethodGet, destination, headers, nil, &settings); err != nil {
		return fmt.Errorf("read Worker settings: %w", err)
	}
	var d1Match, versionMatch, assetsMatch bool
	plainText := make(map[string]string)
	for _, binding := range settings.Bindings {
		d1Match = d1Match || (binding.Type == "d1" && binding.Name == "DB" && binding.DatabaseID == databaseID)
		versionMatch = versionMatch || (binding.Type == "plain_text" && binding.Name == "APP_VERSION" && binding.Text == version)
		assetsMatch = assetsMatch || (binding.Type == "assets" && binding.Name == "ASSETS")
		if binding.Type == "plain_text" {
			plainText[binding.Name] = binding.Text
		}
	}
	if !d1Match || !versionMatch || settings.CompatibilityDate == "" {
		return errors.New("Worker settings do not match the owned D1 database and immutable release")
	}
	if expected != nil {
		if settings.CompatibilityDate != expected.CompatibilityDate || !assetsMatch || len(plainText) != len(expected.Vars) {
			return errors.New("Worker settings do not match the verified bundle contract")
		}
		for name, value := range expected.Vars {
			if plainText[name] != value {
				return errors.New("Worker variables do not match the verified evaluation contract")
			}
		}
	}
	return nil
}

func (c *Cloudflare) verifySchedules(ctx context.Context, account, worker string, expected []string, headers map[string]string) error {
	var schedules struct {
		Schedules []struct {
			Cron string `json:"cron"`
		} `json:"schedules"`
	}
	destination := c.baseURL + "/accounts/" + account + "/workers/scripts/" + worker + "/schedules"
	if err := c.cloudflareJSON(ctx, http.MethodGet, destination, headers, nil, &schedules); err != nil {
		return fmt.Errorf("read Worker schedules: %w", err)
	}
	actual := make([]string, len(schedules.Schedules))
	for index, schedule := range schedules.Schedules {
		actual[index] = schedule.Cron
	}
	if len(actual) != len(expected) {
		return errors.New("Worker schedules do not match the verified bundle contract")
	}
	sort.Strings(actual)
	expectedCopy := append([]string(nil), expected...)
	sort.Strings(expectedCopy)
	for index := range expectedCopy {
		if actual[index] != expectedCopy[index] {
			return errors.New("Worker schedules do not match the verified bundle contract")
		}
	}
	return nil
}

func (c *Cloudflare) verifyManagedHTTPS(ctx context.Context, hostname, version string) error {
	deadline, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		if cloudflareHealthReady(deadline, c.healthClient, hostname, version) {
			return nil
		}
		select {
		case <-deadline.Done():
			return errors.New("Cloudflare Worker did not pass managed HTTPS readiness and release-identity checks within five minutes")
		case <-ticker.C:
		}
	}
}

func cloudflareHealthReady(ctx context.Context, client *http.Client, hostname, version string) bool {
	if !cloudflareManagedHostname.MatchString(hostname) {
		return false
	}
	for _, check := range []struct {
		path    string
		version bool
	}{{path: "/health/ready"}, {path: "/health/live", version: true}} {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+hostname+check.path, nil)
		if err != nil {
			return false
		}
		response, err := client.Do(request)
		if err != nil {
			return false
		}
		var result struct {
			OK      bool   `json:"ok"`
			Version string `json:"version"`
		}
		if decodeHealthResponse(response, &result) != nil || !result.OK || (check.version && result.Version != version) {
			return false
		}
	}
	return true
}

func (c *Cloudflare) Recover(ctx context.Context, kind lifecycle.OperationKind, candidate plan.DeploymentPlan, backup *lifecycle.BackupRecord, credentials Credentials) error {
	if kind != lifecycle.Install || backup != nil {
		return errors.New("Cloudflare recovery currently supports only a failed new evaluation install")
	}
	if strings.TrimSpace(credentials.CloudflareAPIToken) == "" {
		return errors.New("an in-memory Cloudflare API token is required for recovery")
	}
	account, worker, databaseName := cloudflareNames(candidate)
	headers := cloudflareHeaders(credentials)
	databases, err := c.findDatabases(ctx, account, databaseName, headers)
	if err != nil {
		return err
	}
	if len(databases) == 0 {
		return nil
	}
	if len(databases) != 1 {
		return errors.New("refusing recovery because the D1 database identity is ambiguous")
	}
	deploymentID, err := lifecycle.DeploymentID(candidate)
	if err != nil {
		return err
	}
	if err := c.verifyOwnershipMarker(ctx, account, databases[0].UUID, deploymentID, candidate.Release.Version, headers); err != nil {
		return err
	}
	status, _ := controlPlaneJSON(ctx, c.client, http.MethodGet, c.baseURL+"/accounts/"+account+"/workers/scripts/"+worker, headers, nil)
	if status != http.StatusNotFound {
		if status < 200 || status >= 300 {
			return errors.New("failed-install Worker identity could not be inspected")
		}
		if err := c.verifyWorkerSettings(ctx, account, worker, databases[0].UUID, candidate.Release.Version, nil, headers); err != nil {
			return errors.New("refusing to delete a Worker that does not match the owned failed install")
		}
		if err := c.cloudflareJSON(ctx, http.MethodDelete, c.baseURL+"/accounts/"+account+"/workers/scripts/"+worker, headers, nil, nil); err != nil {
			return fmt.Errorf("delete owned failed-install Worker: %w", err)
		}
	}
	destination := c.baseURL + "/accounts/" + account + "/d1/database/" + url.PathEscape(databases[0].UUID)
	if err := c.cloudflareJSON(ctx, http.MethodDelete, destination, headers, nil, nil); err != nil {
		return fmt.Errorf("delete owned failed-install D1 database: %w", err)
	}
	return nil
}

func (c *Cloudflare) cloudflareJSON(ctx context.Context, method, destination string, headers map[string]string, input, output any) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return err
		}
		if len(encoded) > maxControlPlaneResponse {
			return errors.New("Cloudflare JSON request exceeded the safety limit")
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, destination, body)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	return c.doCloudflare(request, output)
}

func (c *Cloudflare) cloudflareMultipart(ctx context.Context, method, destination, token, contentType string, body io.Reader, output any) error {
	request, err := http.NewRequestWithContext(ctx, method, destination, body)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", contentType)
	return c.doCloudflare(request, output)
}

func (c *Cloudflare) doCloudflare(request *http.Request, output any) error {
	response, err := c.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	contents, err := io.ReadAll(io.LimitReader(response.Body, maxControlPlaneResponse+1))
	if err != nil {
		return err
	}
	if len(contents) > maxControlPlaneResponse {
		return errors.New("Cloudflare response exceeded the safety limit")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("Cloudflare returned HTTP %d", response.StatusCode)
	}
	if len(contents) == 0 {
		if output != nil {
			return errors.New("Cloudflare returned an empty response")
		}
		return nil
	}
	var envelope cloudflareEnvelope
	if err := json.Unmarshal(contents, &envelope); err != nil || !envelope.Success {
		return errors.New("Cloudflare returned an unsuccessful or invalid response")
	}
	if output != nil {
		if len(envelope.Result) == 0 || string(envelope.Result) == "null" {
			return errors.New("Cloudflare returned no result")
		}
		if err := json.Unmarshal(envelope.Result, output); err != nil {
			return errors.New("Cloudflare returned an invalid result")
		}
	}
	return nil
}
