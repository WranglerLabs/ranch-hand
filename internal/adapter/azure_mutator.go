package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/WranglerLabs/ranch-hand/internal/bundle"
	"github.com/WranglerLabs/ranch-hand/internal/lifecycle"
	"github.com/WranglerLabs/ranch-hand/internal/plan"
)

const maximumARMTemplate = 16 << 20

var containerAppsFQDNPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]{0,251}[a-z0-9])?\.azurecontainerapps\.io$`)

func (a *AzureContainerApps) Backup(context.Context, plan.DeploymentPlan, string, Credentials) (lifecycle.BackupArtifact, error) {
	return lifecycle.BackupArtifact{}, errors.New("Azure Container Apps backup is not implemented")
}

func (a *AzureContainerApps) Apply(ctx context.Context, kind lifecycle.OperationKind, candidate plan.DeploymentPlan, _ string, staged bundle.StagedBundle, backups lifecycle.OperationBackups, credentials Credentials) error {
	if kind == lifecycle.Uninstall {
		if backups.Selected != nil || backups.Safety != nil {
			return errors.New("Azure uninstall does not accept backup state")
		}
		return a.removeOwnedDeployment(ctx, candidate, credentials)
	}
	if kind != lifecycle.Install || backups.Selected != nil || backups.Safety != nil {
		return errors.New("the Azure Container Apps adapter currently supports only a new evaluation install")
	}
	if err := credentials.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(credentials.AzureAccessToken) == "" {
		return errors.New("an in-memory Azure ARM access token is required")
	}
	if candidate.Configuration["customDomain"] != "" {
		return errors.New("Azure custom-domain certificate binding is not enabled in this evaluation adapter")
	}
	identity, err := bundle.ReadIdentity(staged)
	if err != nil {
		return err
	}
	if staged.Target != "azure-container-apps" {
		return errors.New("Azure adapter requires an azure-container-apps bundle")
	}
	template, err := readARMTemplate(staged)
	if err != nil {
		return err
	}
	deploymentID, err := lifecycle.DeploymentID(candidate)
	if err != nil {
		return err
	}
	headers := azureHeaders(credentials)
	groupURL, deploymentURL, err := a.azureURLs(candidate)
	if err != nil {
		return err
	}
	status, groupErr := controlPlaneJSON(ctx, a.client, http.MethodGet, groupURL, headers, nil)
	if groupErr == nil || status != http.StatusNotFound {
		if groupErr == nil {
			return errors.New("refusing to install into a pre-existing Azure resource group")
		}
		return fmt.Errorf("verify dedicated Azure resource group availability: %w", groupErr)
	}
	group := map[string]any{
		"location": candidate.Configuration["location"],
		"tags": map[string]string{
			"wranglerlabs-ranch-hand-managed": "true", "wranglerlabs-ranch-hand-deployment": deploymentID,
			"wranglerlabs-ranch-hand-version": candidate.Release.Version,
		},
	}
	if _, err := a.armJSON(ctx, http.MethodPut, groupURL, headers, group, nil); err != nil {
		return fmt.Errorf("create dedicated Azure resource group: %w", err)
	}
	parameters := map[string]any{
		"name":                         armValue(candidate.Configuration["appName"]),
		"location":                     armValue(candidate.Configuration["location"]),
		"image":                        armValue(identity.Image),
		"containerAppName":             armValue(candidate.Configuration["appName"]),
		"containerAppsEnvironmentName": armValue(candidate.Configuration["environmentName"]),
		"demoMode":                     armValue(true),
		"postgres":                     armValue(false),
		"keyVaultName":                 armValue(""),
		"authProviders":                armValue("github"),
		"customDomainName":             armValue(""),
		"customDomainCertificateName":  armValue(""),
	}
	deployment := map[string]any{"properties": map[string]any{"mode": "Incremental", "template": template, "parameters": parameters}}
	if _, err := a.armJSON(ctx, http.MethodPut, deploymentURL, headers, deployment, nil); err != nil {
		return fmt.Errorf("start Azure Container Apps ARM deployment: %w", err)
	}
	a.rememberExpectedImage(deploymentID, identity.Image)
	return a.waitForDeployment(ctx, deploymentURL, headers)
}

func (a *AzureContainerApps) removeOwnedDeployment(ctx context.Context, candidate plan.DeploymentPlan, credentials Credentials) error {
	if strings.TrimSpace(credentials.AzureAccessToken) == "" {
		return errors.New("an in-memory Azure ARM access token is required for uninstall")
	}
	deploymentID, err := lifecycle.DeploymentID(candidate)
	if err != nil {
		return err
	}
	groupURL, _, err := a.azureURLs(candidate)
	if err != nil {
		return err
	}
	headers := azureHeaders(credentials)
	var group struct {
		Tags map[string]string `json:"tags"`
	}
	status, readErr := controlPlaneJSON(ctx, a.client, http.MethodGet, groupURL, headers, &group)
	if status == http.StatusNotFound {
		return nil
	}
	if readErr != nil {
		return fmt.Errorf("inspect Azure resource group for uninstall: %w", readErr)
	}
	if group.Tags["wranglerlabs-ranch-hand-managed"] != "true" || group.Tags["wranglerlabs-ranch-hand-deployment"] != deploymentID || group.Tags["wranglerlabs-ranch-hand-version"] != candidate.Release.Version {
		return errors.New("refusing to delete an Azure resource group not owned by this exact Ranch Hand deployment")
	}
	if _, err := a.armJSON(ctx, http.MethodDelete, groupURL, headers, nil, nil); err != nil {
		return fmt.Errorf("delete owned Azure resource group: %w", err)
	}
	deadline, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		status, _ := controlPlaneJSON(deadline, a.client, http.MethodGet, groupURL, headers, nil)
		if status == http.StatusNotFound {
			return nil
		}
		select {
		case <-deadline.Done():
			return errors.New("owned Azure resource group deletion did not complete within 30 minutes")
		case <-ticker.C:
		}
	}
}

func armValue(value any) map[string]any { return map[string]any{"value": value} }

func readARMTemplate(staged bundle.StagedBundle) (json.RawMessage, error) {
	root, err := os.OpenRoot(staged.Path)
	if err != nil {
		return nil, fmt.Errorf("open staged Azure bundle: %w", err)
	}
	defer root.Close()
	details, err := root.Stat("main.json")
	if err != nil || !details.Mode().IsRegular() || details.Size() < 2 || details.Size() > maximumARMTemplate {
		return nil, errors.New("compiled ARM template is not a bounded regular file")
	}
	contents, err := root.ReadFile("main.json")
	if err != nil {
		return nil, fmt.Errorf("read compiled ARM template: %w", err)
	}
	var template map[string]any
	decoder := json.NewDecoder(bytes.NewReader(contents))
	if err := decoder.Decode(&template); err != nil || decoder.Decode(&struct{}{}) != io.EOF || template["$schema"] == nil || template["resources"] == nil {
		return nil, errors.New("compiled ARM template is invalid")
	}
	return json.RawMessage(contents), nil
}

func azureHeaders(credentials Credentials) map[string]string {
	return map[string]string{"Authorization": "Bearer " + credentials.AzureAccessToken}
}

func (a *AzureContainerApps) azureURLs(candidate plan.DeploymentPlan) (string, string, error) {
	if err := candidate.Validate(); err != nil {
		return "", "", err
	}
	subscription := url.PathEscape(candidate.Configuration["subscriptionId"])
	group := url.PathEscape(candidate.Configuration["resourceGroup"])
	base := a.baseURL + "/subscriptions/" + subscription + "/resourcegroups/" + group
	groupURL := base + "?api-version=2022-09-01"
	deploymentName := "ranch-hand-" + strings.NewReplacer(".", "-", "+", "-", "_", "-").Replace(strings.TrimPrefix(candidate.Release.Version, "v"))
	deploymentURL := base + "/providers/Microsoft.Resources/deployments/" + url.PathEscape(deploymentName) + "?api-version=2022-09-01"
	return groupURL, deploymentURL, nil
}

func (a *AzureContainerApps) armJSON(ctx context.Context, method, destination string, headers map[string]string, input, output any) (int, error) {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return 0, err
		}
		if len(encoded) > maximumARMTemplate+1<<20 {
			return 0, errors.New("ARM deployment request exceeded the safety limit")
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, destination, body)
	if err != nil {
		return 0, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := a.client.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	contents, err := io.ReadAll(io.LimitReader(response.Body, maxControlPlaneResponse+1))
	if err != nil {
		return response.StatusCode, err
	}
	if len(contents) > maxControlPlaneResponse {
		return response.StatusCode, errors.New("ARM response exceeded the safety limit")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return response.StatusCode, fmt.Errorf("Azure Resource Manager returned HTTP %d", response.StatusCode)
	}
	if output != nil && len(contents) > 0 {
		if err := json.Unmarshal(contents, output); err != nil {
			return response.StatusCode, errors.New("Azure Resource Manager returned invalid JSON")
		}
	}
	return response.StatusCode, nil
}

func (a *AzureContainerApps) waitForDeployment(ctx context.Context, deploymentURL string, headers map[string]string) error {
	deadline, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		var deployment struct {
			Properties struct {
				ProvisioningState string `json:"provisioningState"`
			} `json:"properties"`
		}
		status, err := controlPlaneJSON(deadline, a.client, http.MethodGet, deploymentURL, headers, &deployment)
		if err == nil {
			switch strings.ToLower(deployment.Properties.ProvisioningState) {
			case "succeeded":
				return nil
			case "failed", "canceled":
				return errors.New("Azure Container Apps deployment did not succeed")
			}
		} else if status == http.StatusUnauthorized || status == http.StatusForbidden {
			return errors.New("Azure authorization expired or does not permit reading the deployment")
		} else if status >= 400 && status < 500 && status != http.StatusNotFound && status != http.StatusRequestTimeout && status != http.StatusConflict && status != http.StatusTooManyRequests {
			return fmt.Errorf("Azure deployment polling returned HTTP %d", status)
		}
		select {
		case <-deadline.Done():
			return errors.New("Azure Container Apps deployment did not complete within 30 minutes")
		case <-ticker.C:
		}
	}
}

func (a *AzureContainerApps) rememberExpectedImage(deploymentID, image string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.expectedImages[deploymentID] = image
}

func (a *AzureContainerApps) expectedImage(deploymentID string) string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.expectedImages[deploymentID]
}

func (a *AzureContainerApps) Verify(ctx context.Context, candidate plan.DeploymentPlan, credentials Credentials) error {
	if strings.TrimSpace(credentials.AzureAccessToken) == "" {
		return errors.New("an in-memory Azure ARM access token is required for verification")
	}
	deploymentID, err := lifecycle.DeploymentID(candidate)
	if err != nil {
		return err
	}
	expectedImage := a.expectedImage(deploymentID)
	if expectedImage == "" {
		return errors.New("the Azure activation image is not bound to this Ranch Hand operation")
	}
	groupURL, _, err := a.azureURLs(candidate)
	if err != nil {
		return err
	}
	resourceBase := strings.Split(groupURL, "?")[0]
	appURL := resourceBase + "/providers/Microsoft.App/containerApps/" + url.PathEscape(candidate.Configuration["appName"]) + "?api-version=2024-03-01"
	var app struct {
		Properties struct {
			ProvisioningState string `json:"provisioningState"`
			Configuration     struct {
				Ingress struct {
					FQDN string `json:"fqdn"`
				} `json:"ingress"`
			} `json:"configuration"`
			Template struct {
				Containers []struct {
					Name  string `json:"name"`
					Image string `json:"image"`
				} `json:"containers"`
			} `json:"template"`
		} `json:"properties"`
	}
	if _, err := controlPlaneJSON(ctx, a.client, http.MethodGet, appURL, azureHeaders(credentials), &app); err != nil {
		return fmt.Errorf("read deployed Azure Container App: %w", err)
	}
	if !strings.EqualFold(app.Properties.ProvisioningState, "Succeeded") || len(app.Properties.Template.Containers) != 1 || app.Properties.Template.Containers[0].Name != "server" || app.Properties.Template.Containers[0].Image != expectedImage {
		return errors.New("Azure Container App activation does not match the verified immutable image")
	}
	fqdn := strings.ToLower(strings.TrimSuffix(app.Properties.Configuration.Ingress.FQDN, "."))
	if !containerAppsFQDNPattern.MatchString(fqdn) {
		return errors.New("Azure Container Apps returned an invalid managed HTTPS hostname")
	}
	return a.verifyManagedHTTPS(ctx, fqdn, candidate.Release.Version)
}

func (a *AzureContainerApps) verifyManagedHTTPS(ctx context.Context, fqdn, expectedVersion string) error {
	deadline, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		if azureHealthReady(deadline, a.healthClient, fqdn, expectedVersion) {
			return nil
		}
		select {
		case <-deadline.Done():
			return errors.New("Azure Container App did not pass managed HTTPS readiness and release-identity checks within five minutes")
		case <-ticker.C:
		}
	}
}

func azureHealthReady(ctx context.Context, client *http.Client, fqdn, expectedVersion string) bool {
	for _, check := range []struct {
		path    string
		version bool
	}{{path: "/health/ready"}, {path: "/health/live", version: true}} {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+fqdn+check.path, nil)
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
		if decodeHealthResponse(response, &result) != nil || !result.OK || (check.version && result.Version != expectedVersion) {
			return false
		}
	}
	return true
}

func (a *AzureContainerApps) Recover(ctx context.Context, kind lifecycle.OperationKind, candidate plan.DeploymentPlan, _ string, backups lifecycle.OperationBackups, credentials Credentials) error {
	if kind == lifecycle.Uninstall {
		if backups.Selected != nil || backups.Safety != nil {
			return errors.New("Azure uninstall recovery does not accept backup state")
		}
		return a.removeOwnedDeployment(ctx, candidate, credentials)
	}
	if kind != lifecycle.Install || backups.Selected != nil || backups.Safety != nil {
		return errors.New("Azure recovery currently supports only a failed new evaluation install")
	}
	if strings.TrimSpace(credentials.AzureAccessToken) == "" {
		return errors.New("an in-memory Azure ARM access token is required for recovery")
	}
	deploymentID, err := lifecycle.DeploymentID(candidate)
	if err != nil {
		return err
	}
	groupURL, _, err := a.azureURLs(candidate)
	if err != nil {
		return err
	}
	headers := azureHeaders(credentials)
	var group struct {
		Tags map[string]string `json:"tags"`
	}
	status, readErr := controlPlaneJSON(ctx, a.client, http.MethodGet, groupURL, headers, &group)
	if status == http.StatusNotFound {
		return nil
	}
	if readErr != nil {
		return fmt.Errorf("inspect failed-install Azure resource group: %w", readErr)
	}
	if group.Tags["wranglerlabs-ranch-hand-managed"] != "true" || group.Tags["wranglerlabs-ranch-hand-deployment"] != deploymentID {
		return errors.New("refusing to delete an Azure resource group not owned by this Ranch Hand deployment")
	}
	if _, err := a.armJSON(ctx, http.MethodDelete, groupURL, headers, nil, nil); err != nil {
		return fmt.Errorf("delete failed-install Azure resource group: %w", err)
	}
	deadline, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		status, _ := controlPlaneJSON(deadline, a.client, http.MethodGet, groupURL, headers, nil)
		if status == http.StatusNotFound {
			return nil
		}
		select {
		case <-deadline.Done():
			return errors.New("owned Azure resource group deletion did not complete within 30 minutes")
		case <-ticker.C:
		}
	}
}
