package release

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"github.com/sigstore/sigstore-go/pkg/testing/ca"
	"github.com/sigstore/sigstore-go/pkg/verify"
)

func verifyVirtualProvenance(entity verify.SignedEntity, virtualSigstore *ca.VirtualSigstore, digest string) error {
	return verifyProvenanceEntityWithOptions(entity, virtualSigstore, digest,
		verify.WithTransparencyLog(1),
		verify.WithObserverTimestamps(1),
	)
}

func TestVerifiesRepoWranglerWorkflowProvenance(t *testing.T) {
	artifact := []byte("verified RepoWrangler bundle")
	digest := sha256.Sum256(artifact)
	digestHex := hex.EncodeToString(digest[:])
	statement := fmt.Sprintf(`{"_type":"https://in-toto.io/Statement/v1","subject":[{"name":"repo-wrangler-compose-v1.2.3.tar.gz","digest":{"sha256":"%s"}}],"predicateType":"%s","predicate":{}}`, digestHex, slsaProvenanceV1)
	virtualSigstore, err := ca.NewVirtualSigstore()
	if err != nil {
		t.Fatal(err)
	}
	entity, err := virtualSigstore.Attest(
		"https://github.com/WranglerLabs/repo-wrangler/.github/workflows/publish-release-artifacts.yml@refs/tags/v1.2.3",
		githubOIDCIssuer,
		[]byte(statement),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyVirtualProvenance(entity, virtualSigstore, digestHex); err != nil {
		t.Fatalf("verify provenance: %v", err)
	}
}

func TestRejectsWrongWorkflowDigestAndPredicate(t *testing.T) {
	artifact := []byte("verified RepoWrangler bundle")
	digest := sha256.Sum256(artifact)
	digestHex := hex.EncodeToString(digest[:])
	virtualSigstore, err := ca.NewVirtualSigstore()
	if err != nil {
		t.Fatal(err)
	}

	statement := func(predicate string) []byte {
		return []byte(fmt.Sprintf(`{"_type":"https://in-toto.io/Statement/v1","subject":[{"name":"bundle","digest":{"sha256":"%s"}}],"predicateType":"%s","predicate":{}}`, digestHex, predicate))
	}
	wrongWorkflow, err := virtualSigstore.Attest("https://github.com/attacker/repo/.github/workflows/build.yml@refs/tags/v1.2.3", githubOIDCIssuer, statement(slsaProvenanceV1))
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyVirtualProvenance(wrongWorkflow, virtualSigstore, digestHex); err == nil {
		t.Fatal("wrong workflow identity was accepted")
	}

	validIdentity := "https://github.com/WranglerLabs/repo-wrangler/.github/workflows/publish-release-artifacts.yml@refs/tags/v1.2.3"
	wrongIssuer, err := virtualSigstore.Attest(validIdentity, "https://attacker.example", statement(slsaProvenanceV1))
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyVirtualProvenance(wrongIssuer, virtualSigstore, digestHex); err == nil {
		t.Fatal("wrong OIDC issuer was accepted")
	}

	wrongPredicate, err := virtualSigstore.Attest(validIdentity, githubOIDCIssuer, statement("https://example.test/untrusted"))
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyVirtualProvenance(wrongPredicate, virtualSigstore, digestHex); err == nil || !strings.Contains(err.Error(), "predicate") {
		t.Fatalf("expected predicate rejection, got %v", err)
	}

	valid, err := virtualSigstore.Attest(validIdentity, githubOIDCIssuer, statement(slsaProvenanceV1))
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyVirtualProvenance(valid, virtualSigstore, strings.Repeat("f", 64)); err == nil {
		t.Fatal("wrong artifact digest was accepted")
	}
}
