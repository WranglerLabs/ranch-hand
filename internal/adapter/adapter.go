package adapter

import (
	"context"
	"errors"
	"fmt"

	"github.com/WranglerLabs/ranch-hand/internal/plan"
)

type Credentials struct {
	AzureAccessToken        string `json:"azureAccessToken,omitempty"`
	CloudflareAPIToken      string `json:"cloudflareApiToken,omitempty"`
	SSHPrivateKey           string `json:"sshPrivateKey,omitempty"`
	SSHPrivateKeyPassphrase string `json:"sshPrivateKeyPassphrase,omitempty"`
	SSHPassword             string `json:"sshPassword,omitempty"`
}

func (c *Credentials) Clear() {
	c.AzureAccessToken = ""
	c.CloudflareAPIToken = ""
	c.SSHPrivateKey = ""
	c.SSHPrivateKeyPassphrase = ""
	c.SSHPassword = ""
}

func (c Credentials) Validate() error {
	if len(c.AzureAccessToken) > 64<<10 || len(c.CloudflareAPIToken) > 64<<10 {
		return errors.New("control-plane token exceeds the 64 KiB safety limit")
	}
	if len(c.SSHPrivateKey) > 1<<20 {
		return errors.New("SSH private key exceeds the 1 MiB safety limit")
	}
	if len(c.SSHPrivateKeyPassphrase) > 8<<10 || len(c.SSHPassword) > 8<<10 {
		return errors.New("SSH passphrase or password exceeds the 8 KiB safety limit")
	}
	return nil
}

type Check struct {
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

type Report struct {
	Ready        bool    `json:"ready"`
	Target       string  `json:"target"`
	State        string  `json:"state,omitempty"`
	DeploymentID string  `json:"deploymentId,omitempty"`
	Checks       []Check `json:"checks"`
}

type Preflighter interface {
	Preflight(context.Context, plan.DeploymentPlan, Credentials) Report
}

type RemnantCleaner interface {
	CleanupRemnant(context.Context, plan.DeploymentPlan, Credentials) error
}

type Registry struct {
	adapters map[string]Preflighter
	cleaners map[string]RemnantCleaner
}

func NewRegistry() *Registry {
	wsl := NewWSLCompose()
	return &Registry{adapters: map[string]Preflighter{
		"azure-container-apps": NewAzureContainerApps(),
		"cloudflare":           NewCloudflare(),
		"local-compose":        NewLocalDocker(),
		"local-wsl-compose":    wsl,
		"remote-linux-compose": NewRemoteLinuxCompose(),
	}, cleaners: map[string]RemnantCleaner{"local-wsl-compose": wsl}}
}

func (r *Registry) CleanupRemnant(ctx context.Context, candidate plan.DeploymentPlan, credentials Credentials) error {
	cleaner, ok := r.cleaners[candidate.Target.Kind]
	if !ok {
		return fmt.Errorf("remnant cleanup is not available for target %q", candidate.Target.Kind)
	}
	return cleaner.CleanupRemnant(ctx, candidate, credentials)
}

func (r *Registry) Preflight(ctx context.Context, candidate plan.DeploymentPlan, credentials Credentials) Report {
	adapter, ok := r.adapters[candidate.Target.Kind]
	if !ok {
		return Report{Target: candidate.Target.Kind, Checks: []Check{{Name: "adapter", OK: false, Message: fmt.Sprintf("No adapter is registered for target %q.", candidate.Target.Kind)}}}
	}
	return adapter.Preflight(ctx, candidate, credentials)
}

func appendCheck(report *Report, name string, ok bool, message string) {
	report.Checks = append(report.Checks, Check{Name: name, OK: ok, Message: message})
}
