package bundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	productrelease "github.com/WranglerLabs/ranch-hand/internal/release"
)

type archiveEntry struct {
	name     string
	contents string
	typeflag byte
}

func writeArchive(t *testing.T, entries []archiveEntry) (string, int64, string) {
	t.Helper()
	var compressed bytes.Buffer
	gzipWriter := gzip.NewWriter(&compressed)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, entry := range entries {
		typeflag := entry.typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}
		header := &tar.Header{Name: entry.name, Mode: 0o644, Size: int64(len(entry.contents)), Typeflag: typeflag}
		if typeflag != tar.TypeReg {
			header.Size = 0
		}
		if typeflag == tar.TypeSymlink {
			header.Linkname = entry.contents
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if typeflag == tar.TypeReg {
			if _, err := tarWriter.Write([]byte(entry.contents)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	filename := filepath.Join(t.TempDir(), "bundle.tar.gz")
	if err := os.WriteFile(filename, compressed.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(compressed.Bytes())
	return filename, int64(compressed.Len()), hex.EncodeToString(digest[:])
}

func composeIdentity(version string) string {
	image := "ghcr.io/wranglerlabs/repo-wrangler-server@sha256:" + strings.Repeat("a", 64)
	postgres := "docker.io/library/postgres@sha256:" + strings.Repeat("b", 64)
	return `{"schemaVersion":"1.0","product":"RepoWrangler","version":"` + version + `","targetFamily":"compose","image":"` + image + `","postgresImage":"` + postgres + `","publicHttps":"operator-provided","defaultBindAddress":"127.0.0.1"}`
}

func verifiedArchive(t *testing.T, entries []archiveEntry) productrelease.VerifiedArtifact {
	t.Helper()
	filename, size, digest := writeArchive(t, entries)
	return productrelease.VerifiedArtifact{
		Product: productrelease.Product, Version: "v1.2.3", Target: "local-compose",
		SHA256: digest, Size: size, CachePath: filename, ProvenanceVerified: true, SBOMVerified: true,
	}
}

func TestStageAndVerifyCacheHit(t *testing.T) {
	verified := verifiedArchive(t, []archiveEntry{
		{name: "compose/bundle.json", contents: composeIdentity("v1.2.3")},
		{name: "compose/compose.yaml", contents: "services: {}\n"},
		{name: "compose/.env.example", contents: "DEMO_MODE=true\n"},
	})
	stager, err := NewStager(filepath.Join(t.TempDir(), "staged"))
	if err != nil {
		t.Fatal(err)
	}
	first, err := stager.Stage(verified)
	if err != nil || first.CacheHit {
		t.Fatalf("first stage failed: %+v, %v", first, err)
	}
	second, err := stager.Stage(verified)
	if err != nil || !second.CacheHit || second.Path != first.Path {
		t.Fatalf("verified cache reuse failed: %+v, %v", second, err)
	}
}

func TestStageRepairsTamperedCache(t *testing.T) {
	verified := verifiedArchive(t, []archiveEntry{
		{name: "compose/bundle.json", contents: composeIdentity("v1.2.3")},
		{name: "compose/compose.yaml", contents: "services: {}\n"},
		{name: "compose/.env.example", contents: "DEMO_MODE=true\n"},
	})
	stager, _ := NewStager(filepath.Join(t.TempDir(), "staged"))
	first, err := stager.Stage(verified)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(first.Path, "compose.yaml"), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(first.Path, stageManifestName)
	manifestJSON, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var forged stageManifest
	if err := json.Unmarshal(manifestJSON, &forged); err != nil {
		t.Fatal(err)
	}
	tamperedDigest := sha256.Sum256([]byte("tampered"))
	for index := range forged.Files {
		if strings.HasSuffix(forged.Files[index].Path, "compose.yaml") {
			forged.Files[index].Size = int64(len("tampered"))
			forged.Files[index].SHA256 = hex.EncodeToString(tamperedDigest[:])
		}
	}
	manifestJSON, _ = json.Marshal(forged)
	if err := os.WriteFile(manifestPath, manifestJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	repaired, err := stager.Stage(verified)
	if err != nil || repaired.CacheHit {
		t.Fatalf("tampered stage was not repaired: %+v, %v", repaired, err)
	}
	contents, _ := os.ReadFile(filepath.Join(repaired.Path, "compose.yaml"))
	if string(contents) != "services: {}\n" {
		t.Fatal("staged file was not restored from verified archive")
	}
}

func TestRejectsTraversalLinkAndWrongRoot(t *testing.T) {
	tests := map[string][]archiveEntry{
		"traversal":  {{name: "compose/../../outside", contents: "bad"}},
		"link":       {{name: "compose/link", contents: "../outside", typeflag: tar.TypeSymlink}},
		"wrong-root": {{name: "cloudflare/bundle.json", contents: `{}`}},
		"reserved":   {{name: "compose/.ranch-hand-stage.json", contents: `{}`}},
	}
	for name, entries := range tests {
		t.Run(name, func(t *testing.T) {
			verified := verifiedArchive(t, entries)
			stager, _ := NewStager(filepath.Join(t.TempDir(), "staged"))
			if _, err := stager.Stage(verified); err == nil {
				t.Fatal("unsafe archive was accepted")
			}
		})
	}
}

func TestRejectsBundleIdentityMismatch(t *testing.T) {
	verified := verifiedArchive(t, []archiveEntry{{name: "compose/bundle.json", contents: composeIdentity("v9.9.9")}})
	stager, _ := NewStager(filepath.Join(t.TempDir(), "staged"))
	if _, err := stager.Stage(verified); err == nil {
		t.Fatal("mismatched bundle identity was accepted")
	}
}

func TestStagesManagedPlatformBundles(t *testing.T) {
	image := "ghcr.io/wranglerlabs/repo-wrangler-server@sha256:" + strings.Repeat("a", 64)
	tests := []struct {
		name     string
		target   string
		root     string
		identity string
		payload  archiveEntry
	}{
		{
			name: "azure-container-apps", target: "azure-container-apps", root: "azure-container-apps",
			identity: `{"schemaVersion":"1.0","product":"RepoWrangler","version":"v1.2.3","targetFamily":"azure-container-apps","image":"` + image + `","publicHttps":"azure-managed-ingress","registryAuthentication":"none-for-public-ghcr"}`,
			payload:  archiveEntry{name: "azure-container-apps/main.json", contents: `{}`},
		},
		{
			name: "cloudflare", target: "cloudflare", root: "cloudflare",
			identity: `{"schemaVersion":"1.0","product":"RepoWrangler","version":"v1.2.3","targetFamily":"cloudflare","worker":"worker.js","assetsDirectory":"assets","migrationsDirectory":"migrations","compatibilityDate":"2026-07-01","publicHttps":"cloudflare-managed","assetsBinding":"ASSETS","d1Binding":"DB","assetsNotFoundHandling":"single-page-application","assetsRunWorkerFirst":["/api/*","/auth/*","/webhooks/*","/health/*","/setup/*"],"crons":["*/5 * * * *","17 3 * * *"],"vars":{"ALLOWED_GITHUB_USERS":"","APP_VERSION":"v1.2.3","AUTH_MODE":"github_app","DEMO_MODE":"true"},"observabilityEnabled":true}`,
			payload:  archiveEntry{name: "cloudflare/worker.js", contents: "export default {}"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			entries := []archiveEntry{{name: test.root + "/bundle.json", contents: test.identity}, test.payload}
			if test.target == "cloudflare" {
				entries = append(entries,
					archiveEntry{name: "cloudflare/assets", typeflag: tar.TypeDir},
					archiveEntry{name: "cloudflare/migrations", typeflag: tar.TypeDir},
				)
			}
			verified := verifiedArchive(t, entries)
			verified.Target = test.target
			stager, _ := NewStager(filepath.Join(t.TempDir(), "staged"))
			if _, err := stager.Stage(verified); err != nil {
				t.Fatalf("managed platform bundle rejected: %v", err)
			}
		})
	}
}

func TestRejectsSymlinkedStagingComponent(t *testing.T) {
	verified := verifiedArchive(t, []archiveEntry{
		{name: "compose/bundle.json", contents: composeIdentity("v1.2.3")},
		{name: "compose/compose.yaml", contents: "services: {}\n"},
		{name: "compose/.env.example", contents: "DEMO_MODE=true\n"},
	})
	root := filepath.Join(t.TempDir(), "staged")
	stager, err := NewStager(root)
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "v1.2.3")); err != nil {
		t.Skipf("directory symlinks unavailable: %v", err)
	}
	if _, err := stager.Stage(verified); err == nil {
		t.Fatal("symlinked staging path was accepted")
	}
}
