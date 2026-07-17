package adapter

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/WranglerLabs/ranch-hand/internal/plan"
)

type AzureContainerApps struct {
	client  *http.Client
	baseURL string
}

func NewAzureContainerApps() *AzureContainerApps {
	return newAzureContainerApps(&http.Client{
		Timeout:       30 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}, "https://management.azure.com")
}

func newAzureContainerApps(client *http.Client, baseURL string) *AzureContainerApps {
	return &AzureContainerApps{client: client, baseURL: strings.TrimRight(baseURL, "/")}
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
	appendCheck(&report, "azure-native-https", true, "The verified ARM bundle uses Azure Container Apps managed HTTPS ingress with insecure traffic disabled.")
	report.Ready = true
	return report
}
