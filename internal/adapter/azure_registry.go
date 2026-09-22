package adapter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/WranglerLabs/ranch-hand/internal/bundle"
	"github.com/WranglerLabs/ranch-hand/internal/plan"
)

const maximumRegistryManifest = 16 << 20

var publicGHCRImagePattern = regexp.MustCompile(`^ghcr\.io/([a-z0-9]+(?:[._-][a-z0-9]+)*(?:/[a-z0-9]+(?:[._-][a-z0-9]+)*)+)@(sha256:[a-f0-9]{64})$`)

func (a *AzureContainerApps) PreflightStaged(ctx context.Context, candidate plan.DeploymentPlan, staged bundle.StagedBundle, credentials Credentials) Report {
	report := a.Preflight(ctx, candidate, credentials)
	if !report.Ready {
		return report
	}
	identity, err := bundle.ReadIdentity(staged)
	if err != nil || staged.Target != "azure-container-apps" {
		report.Ready = false
		appendCheck(&report, "azure-release-image", false, "The staged Azure release identity is invalid.")
		return report
	}
	if a.verifyPublicImage == nil {
		err = errors.New("public image verifier is unavailable")
	} else {
		err = a.verifyPublicImage(ctx, identity.Image)
	}
	if err != nil {
		report.Ready = false
		appendCheck(&report, "azure-release-image", false, "The exact release image is not anonymously pullable by Azure Container Apps: "+err.Error())
		return report
	}
	appendCheck(&report, "azure-release-image", true, "The exact digest-pinned release image is anonymously pullable by Azure Container Apps.")
	return report
}

func verifyPublicGHCRImage(ctx context.Context, client *http.Client, image string) error {
	match := publicGHCRImagePattern.FindStringSubmatch(image)
	if match == nil {
		return errors.New("image must be a digest-pinned ghcr.io reference")
	}
	repository, digest := match[1], match[2]
	tokenURL := &url.URL{Scheme: "https", Host: "ghcr.io", Path: "/token"}
	query := tokenURL.Query()
	query.Set("service", "ghcr.io")
	query.Set("scope", "repository:"+repository+":pull")
	tokenURL.RawQuery = query.Encode()
	tokenRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, tokenURL.String(), nil)
	if err != nil {
		return err
	}
	tokenRequest.Header.Set("Accept", "application/json")
	tokenResponse, err := client.Do(tokenRequest)
	if err != nil {
		return fmt.Errorf("anonymous registry token request failed: %w", err)
	}
	defer tokenResponse.Body.Close()
	if tokenResponse.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(tokenResponse.Body, 64<<10))
		return fmt.Errorf("anonymous registry token request failed with HTTP %d", tokenResponse.StatusCode)
	}
	var tokenPayload struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	decoder := json.NewDecoder(io.LimitReader(tokenResponse.Body, 64<<10))
	if err := decoder.Decode(&tokenPayload); err != nil {
		return errors.New("anonymous registry token response was invalid")
	}
	token := tokenPayload.Token
	if token == "" {
		token = tokenPayload.AccessToken
	}
	if token == "" || len(token) > 64<<10 {
		return errors.New("anonymous registry token response did not contain a bounded token")
	}
	manifestURL := "https://ghcr.io/v2/" + repository + "/manifests/" + digest
	manifestRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, manifestURL, nil)
	if err != nil {
		return err
	}
	manifestRequest.Header.Set("Authorization", "Bearer "+token)
	manifestRequest.Header.Set("Accept", strings.Join([]string{
		"application/vnd.oci.image.index.v1+json",
		"application/vnd.docker.distribution.manifest.list.v2+json",
		"application/vnd.oci.image.manifest.v1+json",
		"application/vnd.docker.distribution.manifest.v2+json",
	}, ", "))
	manifestResponse, err := client.Do(manifestRequest)
	if err != nil {
		return fmt.Errorf("anonymous manifest request failed: %w", err)
	}
	defer manifestResponse.Body.Close()
	if manifestResponse.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(manifestResponse.Body, 64<<10))
		return fmt.Errorf("registry returned HTTP %d", manifestResponse.StatusCode)
	}
	contents, err := io.ReadAll(io.LimitReader(manifestResponse.Body, maximumRegistryManifest+1))
	if err != nil || len(contents) == 0 || len(contents) > maximumRegistryManifest {
		return errors.New("registry returned an invalid or oversized manifest")
	}
	hash := sha256.Sum256(contents)
	if "sha256:"+hex.EncodeToString(hash[:]) != digest {
		return errors.New("registry manifest content does not match the requested immutable digest")
	}
	return nil
}
