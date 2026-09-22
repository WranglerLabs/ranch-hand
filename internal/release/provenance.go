package release

import (
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/sigstore/protobuf-specs/gen/pb-go/bundle/v1"
	"github.com/sigstore/sigstore-go/pkg/bundle"
	"github.com/sigstore/sigstore-go/pkg/fulcio/certificate"
	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/tuf"
	"github.com/sigstore/sigstore-go/pkg/verify"
	"google.golang.org/protobuf/encoding/protojson"
)

const (
	slsaProvenanceV1 = "https://slsa.dev/provenance/v1"
	githubOIDCIssuer = "https://token.actions.githubusercontent.com"
	workflowSANRegex = `^https://github\.com/WranglerLabs/repo-wrangler/\.github/workflows/publish-release-artifacts\.yml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+(?:[-+][A-Za-z0-9.-]+)?$`
)

type ProvenanceVerifier interface {
	Verify(bundleJSON []byte, artifactSHA256 string) error
}

type SigstoreProvenanceVerifier struct {
	trustCache string
	mu         sync.Mutex
	verifier   *verify.Verifier
}

func NewSigstoreProvenanceVerifier(cacheRoot string) (*SigstoreProvenanceVerifier, error) {
	if cacheRoot == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			return nil, fmt.Errorf("locate user cache: %w", err)
		}
		cacheRoot = filepath.Join(base, "WranglerLabs", "Ranch Hand")
	}
	return &SigstoreProvenanceVerifier{trustCache: filepath.Join(cacheRoot, "trust", "sigstore")}, nil
}

func (v *SigstoreProvenanceVerifier) Verify(bundleJSON []byte, artifactSHA256 string) error {
	if !digestPattern.MatchString(artifactSHA256) {
		return errors.New("provenance subject SHA-256 must contain 64 hexadecimal characters")
	}
	var protobufBundle v1.Bundle
	if err := protojson.Unmarshal(bundleJSON, &protobufBundle); err != nil {
		return fmt.Errorf("decode Sigstore provenance bundle: %w", err)
	}
	signedBundle, err := bundle.NewBundle(&protobufBundle)
	if err != nil {
		return fmt.Errorf("load Sigstore provenance bundle: %w", err)
	}

	verifier, err := v.loadVerifier()
	if err != nil {
		return err
	}
	return verifyProvenanceBundle(signedBundle, verifier, artifactSHA256)
}

func (v *SigstoreProvenanceVerifier) loadVerifier() (*verify.Verifier, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.verifier != nil {
		return v.verifier, nil
	}
	options := tuf.DefaultOptions()
	options.CachePath = v.trustCache
	options.CacheValidity = 1
	client, err := tuf.New(options)
	if err != nil {
		return nil, fmt.Errorf("initialize Sigstore trust metadata: %w", err)
	}
	trustedRoot, err := root.GetTrustedRoot(client)
	if err != nil {
		return nil, fmt.Errorf("update Sigstore trust metadata: %w", err)
	}
	v.verifier, err = verify.NewVerifier(
		trustedRoot,
		verify.WithSignedCertificateTimestamps(1),
		verify.WithTransparencyLog(1),
		verify.WithObserverTimestamps(1),
	)
	if err != nil {
		return nil, fmt.Errorf("initialize provenance verifier: %w", err)
	}
	return v.verifier, nil
}

func verifyProvenanceEntity(entity verify.SignedEntity, trustedMaterial root.TrustedMaterial, artifactSHA256 string) error {
	return verifyProvenanceEntityWithOptions(entity, trustedMaterial, artifactSHA256,
		verify.WithSignedCertificateTimestamps(1),
		verify.WithTransparencyLog(1),
		verify.WithObserverTimestamps(1),
	)
}

func verifyProvenanceEntityWithOptions(entity verify.SignedEntity, trustedMaterial root.TrustedMaterial, artifactSHA256 string, options ...verify.VerifierOption) error {
	verifier, err := verify.NewVerifier(trustedMaterial, options...)
	if err != nil {
		return fmt.Errorf("initialize provenance verifier: %w", err)
	}
	return verifyProvenanceBundle(entity, verifier, artifactSHA256)
}

func verifyProvenanceBundle(entity verify.SignedEntity, verifier *verify.Verifier, artifactSHA256 string) error {
	digest, err := hex.DecodeString(artifactSHA256)
	if err != nil || len(digest) != 32 {
		return errors.New("provenance subject SHA-256 is invalid")
	}
	san, err := verify.NewSANMatcher("", workflowSANRegex)
	if err != nil {
		return err
	}
	issuer, err := verify.NewIssuerMatcher(githubOIDCIssuer, "")
	if err != nil {
		return err
	}
	identity, err := verify.NewCertificateIdentity(san, issuer, certificate.Extensions{})
	if err != nil {
		return err
	}
	result, err := verifier.Verify(entity, verify.NewPolicy(
		verify.WithArtifactDigest("sha256", digest),
		verify.WithCertificateIdentity(identity),
	))
	if err != nil {
		return fmt.Errorf("Sigstore provenance verification failed: %w", err)
	}
	if result.Statement == nil || result.Statement.PredicateType != slsaProvenanceV1 {
		return fmt.Errorf("provenance predicate must be %q", slsaProvenanceV1)
	}
	return nil
}
