package plan

import "testing"

const validPlan = `{
  "schemaVersion":"1.0",
  "name":"My RepoWrangler",
  "release":{"version":"v1.0.8","manifestUrl":"https://github.com/WranglerLabs/repo-wrangler/releases/download/v1.0.8/release-manifest.json"},
  "target":{"kind":"azure-container-apps"},
  "configuration":{"region":"eastus"}
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

func TestRejectsSecretsAtAnyDepth(t *testing.T) {
	data := []byte(`{"schemaVersion":"1.0","name":"bad","release":{"version":"v1.0.8","manifestUrl":"https://example.test/manifest.json"},"target":{"kind":"cloudflare"},"configuration":{"nested":{"apiToken":"nope"}}}`)
	if _, err := DecodeAndValidate(data); err == nil {
		t.Fatal("expected secret field to be rejected")
	}
}

func TestRejectsFloatingRelease(t *testing.T) {
	data := []byte(`{"schemaVersion":"1.0","name":"bad","release":{"version":"latest","manifestUrl":"https://example.test/manifest.json"},"target":{"kind":"cloudflare"}}`)
	if _, err := DecodeAndValidate(data); err == nil {
		t.Fatal("expected floating release to be rejected")
	}
}
