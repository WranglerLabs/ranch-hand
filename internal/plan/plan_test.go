package plan

import (
	"encoding/json"
	"testing"
)

const validPlan = `{
  "schemaVersion":"1.0",
  "name":"My RepoWrangler",
  "release":{"version":"v1.0.8","manifestUrl":"https://github.com/WranglerLabs/repo-wrangler/releases/download/v1.0.8/release-manifest.json","manifestSha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","artifactSha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","artifactSize":42},
  "target":{"kind":"azure-container-apps"},
  "configuration":{"subscriptionId":"00000000-0000-0000-0000-000000000000","resourceGroup":"rg-ranch-hand","location":"eastus","environmentName":"cae-ranch-hand","appName":"repo-wrangler"}
}`

func TestDecodeAndValidate(t *testing.T) {
	plan, err := DecodeAndValidate([]byte(validPlan))
	if err != nil {
		t.Fatalf("expected valid plan: %v", err)
	}
	if plan.Target.Kind != "azure-container-apps" {
		t.Fatalf("unexpected target: %s", plan.Target.Kind)
	}
}

func TestRejectsInvalidAzureTargetIdentifiers(t *testing.T) {
	var candidate DeploymentPlan
	if err := json.Unmarshal([]byte(validPlan), &candidate); err != nil {
		t.Fatal(err)
	}
	candidate.Configuration["subscriptionId"] = "not-a-subscription"
	candidate.Configuration["appName"] = "Invalid App"
	if err := candidate.Validate(); err == nil {
		t.Fatal("invalid Azure subscription and Container App names were accepted")
	}
}

func TestRejectsSecretsAtAnyDepth(t *testing.T) {
	data := []byte(`{"schemaVersion":"1.0","name":"bad","release":{"version":"v1.0.8","manifestUrl":"https://example.test/manifest.json","manifestSha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","artifactSha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","artifactSize":42},"target":{"kind":"cloudflare"},"configuration":{"nested":{"apiToken":"nope"}}}`)
	if _, err := DecodeAndValidate(data); err == nil {
		t.Fatal("expected secret field to be rejected")
	}
}

func TestRejectsFloatingRelease(t *testing.T) {
	data := []byte(`{"schemaVersion":"1.0","name":"bad","release":{"version":"latest","manifestUrl":"https://example.test/manifest.json","manifestSha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","artifactSha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","artifactSize":42},"target":{"kind":"cloudflare"}}`)
	if _, err := DecodeAndValidate(data); err == nil {
		t.Fatal("expected floating release to be rejected")
	}
}

func TestCreateBindsPlanToVerifiedRelease(t *testing.T) {
	candidate, err := Create(CreateRequest{
		Name: "Cloud Wrangler", Version: "v1.2.3", Target: "cloudflare",
		Configuration: map[string]string{"accountId": "0123456789abcdef0123456789abcdef", "workerName": "wrangler", "databaseName": "wrangler-db"},
	}, VerifiedRelease{
		ManifestURL:    "https://github.com/WranglerLabs/repo-wrangler/releases/download/v1.2.3/release-manifest.json",
		ManifestSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ArtifactSHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		ArtifactSize:   42,
	})
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Release.ArtifactSHA256 == "" || candidate.Release.ArtifactSize != 42 {
		t.Fatalf("plan is not bound to verified artifact: %+v", candidate.Release)
	}
	first, err := CanonicalJSON(candidate)
	if err != nil {
		t.Fatal(err)
	}
	second, err := CanonicalJSON(candidate)
	if err != nil || string(first) != string(second) {
		t.Fatal("canonical deployment plan is not deterministic")
	}
}

func TestRejectsUnsafeCloudflareIdentifiers(t *testing.T) {
	var candidate DeploymentPlan
	if err := json.Unmarshal([]byte(validPlan), &candidate); err != nil {
		t.Fatal(err)
	}
	candidate.Target.Kind = "cloudflare"
	candidate.Configuration = map[string]string{"accountId": "account", "workerName": "Invalid_Worker", "databaseName": "database"}
	if err := candidate.Validate(); err == nil {
		t.Fatal("unsafe Cloudflare account and Worker identifiers were accepted")
	}
}

func TestValidatesRemoteLinuxBoundary(t *testing.T) {
	var candidate DeploymentPlan
	if err := json.Unmarshal([]byte(validPlan), &candidate); err != nil {
		t.Fatal(err)
	}
	candidate.Target.Kind = "remote-linux-compose"
	candidate.Configuration = map[string]string{
		"host": "server.example.com", "port": "22", "user": "repo-wrangler", "installDirectory": "/opt/repo-wrangler",
		"projectName": "repo-wrangler", "hostKeySha256": "SHA256:" + "Aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	if err := candidate.Validate(); err != nil {
		t.Fatalf("valid remote Linux boundary rejected: %v", err)
	}
	candidate.Configuration["installDirectory"] = "/"
	candidate.Configuration["hostKeySha256"] = "accept-any-host"
	if err := candidate.Validate(); err == nil {
		t.Fatal("unsafe remote Linux path and host fingerprint were accepted")
	}
}

func TestRejectsUnknownConfigurationField(t *testing.T) {
	var candidate DeploymentPlan
	if err := json.Unmarshal([]byte(validPlan), &candidate); err != nil {
		t.Fatal(err)
	}
	candidate.Configuration["tenantId"] = "not-supported"
	if err := candidate.Validate(); err == nil {
		t.Fatal("expected unknown configuration field to be rejected")
	}
}

func TestRejectsUnsafeLocalComposeInputs(t *testing.T) {
	var candidate DeploymentPlan
	if err := json.Unmarshal([]byte(validPlan), &candidate); err != nil {
		t.Fatal(err)
	}
	candidate.Target.Kind = "local-compose"
	candidate.Configuration = map[string]string{"projectName": "Repo Wrangler", "dataVolume": "Repo Wrangler Data", "listenAddress": "0.0.0.0:8080"}
	if err := candidate.Validate(); err == nil {
		t.Fatal("unsafe local Compose inputs were accepted")
	}
}
