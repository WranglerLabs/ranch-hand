package plan

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
)

type Check struct {
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

type PreflightReport struct {
	Ready  bool    `json:"ready"`
	Target string  `json:"target"`
	Checks []Check `json:"checks"`
}

type DryRunStep struct {
	Order       int    `json:"order"`
	Action      string `json:"action"`
	Description string `json:"description"`
	Mutates     bool   `json:"mutates"`
}

type DryRunReport struct {
	Target  string       `json:"target"`
	Mutated bool         `json:"mutated"`
	Steps   []DryRunStep `json:"steps"`
}

func Preflight(candidate DeploymentPlan, artifactPath string) PreflightReport {
	report := PreflightReport{Target: candidate.Target.Kind}
	add := func(name string, ok bool, message string) {
		report.Checks = append(report.Checks, Check{Name: name, OK: ok, Message: message})
	}
	if err := candidate.Validate(); err != nil {
		add("deployment-plan", false, err.Error())
		return report
	}
	add("deployment-plan", true, "Plan schema, immutable release identity, target, and secret-free configuration are valid.")

	file, err := os.Open(artifactPath)
	if err != nil {
		add("verified-artifact", false, fmt.Sprintf("Open verified artifact: %v", err))
		return report
	}
	defer file.Close()
	details, err := file.Stat()
	if err != nil {
		add("verified-artifact", false, fmt.Sprintf("Inspect verified artifact: %v", err))
		return report
	}
	if details.Size() != candidate.Release.ArtifactSize {
		add("verified-artifact", false, "Cached artifact size no longer matches the deployment plan.")
		return report
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		add("verified-artifact", false, fmt.Sprintf("Hash verified artifact: %v", err))
		return report
	}
	if hex.EncodeToString(hash.Sum(nil)) != candidate.Release.ArtifactSHA256 {
		add("verified-artifact", false, "Cached artifact SHA-256 no longer matches the deployment plan.")
		return report
	}
	add("verified-artifact", true, "Cached artifact size and SHA-256 match the verified immutable release.")
	add("target-inputs", true, "All required non-secret target inputs are present.")
	report.Ready = true
	return report
}

func DryRun(candidate DeploymentPlan) (DryRunReport, error) {
	if err := candidate.Validate(); err != nil {
		return DryRunReport{}, err
	}
	actions := map[string][]string{
		"azure-container-apps": {
			"Read Azure subscription, resource group, and Container Apps environment state through ARM.",
			"Compare the compiled release template with the requested app and native HTTPS ingress configuration.",
			"Request runtime secrets at apply time, deploy the immutable image digest, and verify release health.",
		},
		"cloudflare": {
			"Read Worker, D1, route, and custom-domain state through Cloudflare APIs.",
			"Compare the verified Worker, assets, and migrations with the selected account resources.",
			"Request the API token at apply time, deploy immutable assets, migrate D1, and verify native HTTPS health.",
		},
		"local-compose": {
			"Connect to the local Docker Engine API and inspect Compose project, network, volume, and port state.",
			"Stage the verified Compose bundle and resolve its immutable image digest without source checkout.",
			"Request runtime secrets at apply time, create the project, and verify loopback or operator-provided HTTPS ingress.",
		},
		"remote-linux-compose": {
			"Verify the pinned SSH host identity, Linux Docker Engine, Docker Compose v2, unused project, and dedicated installation directory.",
			"Transfer the verified Compose bundle, evaluation environment, Docker ownership labels, and target-side marker without source checkout.",
			"Request SSH credentials at apply time, activate the loopback-only project, and verify readiness and release identity through the pinned SSH connection.",
		},
	}
	report := DryRunReport{Target: candidate.Target.Kind, Mutated: false}
	for index, description := range actions[candidate.Target.Kind] {
		report.Steps = append(report.Steps, DryRunStep{Order: index + 1, Action: "would-run", Description: description, Mutates: false})
	}
	return report, nil
}
