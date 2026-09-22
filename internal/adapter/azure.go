package adapter

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/WranglerLabs/ranch-hand/internal/plan"
)

type AzureContainerApps struct {
	client            *http.Client
	healthClient      *http.Client
	baseURL           string
	mu                sync.RWMutex
	expectedImages    map[string]string
	verifyPublicImage func(context.Context, string) error
}

func NewAzureContainerApps() *AzureContainerApps {
	return newAzureContainerApps(&http.Client{
		Timeout:       30 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}, "https://management.azure.com")
}

func newAzureContainerApps(client *http.Client, baseURL string) *AzureContainerApps {
	registryClient := &http.Client{
		Timeout:       30 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}
	return &AzureContainerApps{
		client: client, healthClient: client, baseURL: strings.TrimRight(baseURL, "/"), expectedImages: make(map[string]string),
		verifyPublicImage: func(ctx context.Context, image string) error {
			return verifyPublicGHCRImage(ctx, registryClient, image)
		},
	}
}

func (a *AzureContainerApps) Preflight(ctx context.Context, candidate plan.DeploymentPlan, credentials Credentials) Report {
	report := Report{Target: candidate.Target.Kind}
	if strings.TrimSpace(credentials.AzureAccessToken) == "" {
		appendCheck(&report, "azure-authentication", false, "An Azure ARM access token is required in memory for this live preflight.")
		return report
	}
	headers := map[string]string{"Authorization": "Bearer " + credentials.AzureAccessToken}
	subscriptionID := url.PathEscape(candidate.Configuration["subscriptionId"])
	var subscription struct {
		ID    string `json:"id"`
		State string `json:"state"`
	}
	_, err := controlPlaneJSON(ctx, a.client, http.MethodGet, a.baseURL+"/subscriptions/"+subscriptionID+"?api-version=2022-12-01", headers, &subscription)
	if err != nil {
		appendCheck(&report, "azure-subscription", false, "Azure Resource Manager could not read the selected subscription: "+err.Error())
		return report
	}
	appendCheck(&report, "azure-subscription", true, "Azure Resource Manager authenticated and can read the selected subscription.")

	var provider struct {
		RegistrationState string `json:"registrationState"`
	}
	_, err = controlPlaneJSON(ctx, a.client, http.MethodGet, a.baseURL+"/subscriptions/"+subscriptionID+"/providers/Microsoft.App?api-version=2021-04-01", headers, &provider)
	if err != nil {
		appendCheck(&report, "azure-container-apps-provider", false, "Azure Resource Manager could not inspect the Microsoft.App provider: "+err.Error())
		return report
	}
	if !strings.EqualFold(provider.RegistrationState, "Registered") {
		appendCheck(&report, "azure-container-apps-provider", false, "The Microsoft.App resource provider is not registered in the selected subscription.")
		return report
	}
	appendCheck(&report, "azure-container-apps-provider", true, "The Microsoft.App resource provider is registered.")
	resourceGroupURL := a.baseURL + "/subscriptions/" + subscriptionID + "/resourcegroups/" + url.PathEscape(candidate.Configuration["resourceGroup"]) + "?api-version=2022-09-01"
	status, groupErr := controlPlaneJSON(ctx, a.client, http.MethodGet, resourceGroupURL, headers, nil)
	if groupErr == nil || status != http.StatusNotFound {
		if groupErr == nil {
			appendCheck(&report, "azure-dedicated-resource-group", false, "The first Ranch Hand Azure adapter requires a new dedicated resource group so failed-install recovery cannot affect unrelated resources.")
		} else {
			appendCheck(&report, "azure-dedicated-resource-group", false, "Azure Resource Manager could not verify that the dedicated resource group is available: "+groupErr.Error())
		}
		return report
	}
	if candidate.Configuration["customDomain"] != "" {
		appendCheck(&report, "azure-custom-domain", false, "Custom-domain certificate binding is not enabled in this evaluation adapter; use the Azure-managed Container Apps hostname.")
		return report
	}
	appendCheck(&report, "azure-dedicated-resource-group", true, "The selected dedicated resource group name is available.")
	appendCheck(&report, "azure-native-https", true, "The verified ARM bundle uses Azure Container Apps managed HTTPS ingress with insecure traffic disabled.")
	report.Ready = true
	return report
}
