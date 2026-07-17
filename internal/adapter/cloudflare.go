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

type Cloudflare struct {
	client       *http.Client
	healthClient *http.Client
	baseURL      string
	mu           sync.RWMutex
	expected     map[string]cloudflareExpected
}

func NewCloudflare() *Cloudflare {
	client := &http.Client{
		Timeout:       30 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}
	adapter := newCloudflare(client, "https://api.cloudflare.com/client/v4")
	adapter.healthClient = client
	return adapter
}

func newCloudflare(client *http.Client, baseURL string) *Cloudflare {
	return &Cloudflare{client: client, healthClient: client, baseURL: strings.TrimRight(baseURL, "/"), expected: make(map[string]cloudflareExpected)}
}

func (c *Cloudflare) Preflight(ctx context.Context, candidate plan.DeploymentPlan, credentials Credentials) Report {
	report := Report{Target: candidate.Target.Kind}
	if err := candidate.Validate(); err != nil {
		appendCheck(&report, "cloudflare-plan", false, "The Cloudflare deployment plan is invalid: "+err.Error())
		return report
	}
	if candidate.Configuration["customDomain"] != "" {
		appendCheck(&report, "cloudflare-custom-domain", false, "Custom-domain binding is not enabled in the evaluation adapter; use Cloudflare-managed workers.dev HTTPS.")
		return report
	}
	if strings.TrimSpace(credentials.CloudflareAPIToken) == "" {
		appendCheck(&report, "cloudflare-authentication", false, "A scoped Cloudflare API token is required in memory for this live preflight.")
		return report
	}
	headers := map[string]string{"Authorization": "Bearer " + credentials.CloudflareAPIToken}
	var tokenResult struct {
		Success bool `json:"success"`
		Result  struct {
			Status string `json:"status"`
		} `json:"result"`
	}
	_, err := controlPlaneJSON(ctx, c.client, http.MethodGet, c.baseURL+"/user/tokens/verify", headers, &tokenResult)
	if err != nil || !tokenResult.Success || !strings.EqualFold(tokenResult.Result.Status, "active") {
		if err != nil {
			appendCheck(&report, "cloudflare-token", false, "Cloudflare rejected or could not verify the API token: "+err.Error())
		} else {
			appendCheck(&report, "cloudflare-token", false, "Cloudflare reports that the API token is not active.")
		}
		return report
	}
	appendCheck(&report, "cloudflare-token", true, "Cloudflare reports that the in-memory API token is active.")

	var accountResult struct {
		Success bool `json:"success"`
		Result  struct {
			ID string `json:"id"`
		} `json:"result"`
	}
	accountID := url.PathEscape(candidate.Configuration["accountId"])
	_, err = controlPlaneJSON(ctx, c.client, http.MethodGet, c.baseURL+"/accounts/"+accountID, headers, &accountResult)
	if err != nil || !accountResult.Success || accountResult.Result.ID != candidate.Configuration["accountId"] {
		if err != nil {
			appendCheck(&report, "cloudflare-account", false, "Cloudflare could not read the selected account: "+err.Error())
		} else {
			appendCheck(&report, "cloudflare-account", false, "Cloudflare returned no selected account identity.")
		}
		return report
	}
	appendCheck(&report, "cloudflare-account", true, "Cloudflare authenticated and can read the selected account.")

	var subdomainResult struct {
		Success bool `json:"success"`
		Result  struct {
			Subdomain string `json:"subdomain"`
		} `json:"result"`
	}
	_, err = controlPlaneJSON(ctx, c.client, http.MethodGet, c.baseURL+"/accounts/"+accountID+"/workers/subdomain", headers, &subdomainResult)
	if err != nil || !subdomainResult.Success || strings.TrimSpace(subdomainResult.Result.Subdomain) == "" {
		appendCheck(&report, "cloudflare-workers-dev", false, "The selected account does not expose a readable workers.dev subdomain.")
		return report
	}
	appendCheck(&report, "cloudflare-workers-dev", true, "The selected account has a Cloudflare-managed workers.dev HTTPS subdomain.")
	if err := c.requireAvailable(ctx, accountID, url.PathEscape(candidate.Configuration["workerName"]), candidate.Configuration["databaseName"], headers); err != nil {
		appendCheck(&report, "cloudflare-dedicated-resources", false, err.Error())
		return report
	}
	appendCheck(&report, "cloudflare-dedicated-resources", true, "The requested Worker and D1 names are unused and form a dedicated evaluation boundary.")
	appendCheck(&report, "cloudflare-native-https", true, "The verified Worker bundle uses Cloudflare-managed HTTPS; Ranch Hand does not install a proxy.")
	report.Ready = true
	return report
}
