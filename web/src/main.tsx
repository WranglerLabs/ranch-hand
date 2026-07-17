import React, { useEffect, useState } from "react";
import { createRoot } from "react-dom/client";
import "./styles.css";

type Status = {
  name: string;
  version: string;
  apiVersion: string;
  platform: string;
};

type VerifiedArtifact = {
  product: string;
  version: string;
  target: string;
  sha256: string;
  size: number;
  cachePath: string;
  cacheHit: boolean;
  provenanceVerified: boolean;
  sbomVerified: boolean;
  manifestUrl: string;
  manifestSha256: string;
};

type DeploymentPlan = {
  schemaVersion: string;
  name: string;
  release: { version: string; manifestUrl: string; manifestSha256: string; artifactSha256: string; artifactSize: number };
  target: { kind: string };
  configuration: Record<string, string>;
};

type PreflightReport = { ready: boolean; checks: { name: string; ok: boolean; message: string }[] };
type DryRunReport = { mutated: boolean; steps: { order: number; description: string; mutates: boolean }[] };
type TargetReport = { ready: boolean; target: string; checks: { name: string; ok: boolean; message: string }[] };
type StagedBundle = { product: string; version: string; target: string; path: string; cacheHit: boolean };
type OperationResult = { completed: boolean; operation: { journal: { phase: string }; backup?: { artifact: { locator: string; size: number; sha256: string } } } };

const targetFields: Record<string, { key: string; label: string; placeholder: string; optional?: boolean }[]> = {
  "azure-container-apps": [
    { key: "subscriptionId", label: "Subscription ID", placeholder: "00000000-0000-0000-0000-000000000000" },
    { key: "resourceGroup", label: "Resource group", placeholder: "rg-repo-wrangler" },
    { key: "location", label: "Azure region", placeholder: "eastus" },
    { key: "environmentName", label: "Container Apps environment", placeholder: "cae-repo-wrangler" },
    { key: "appName", label: "Container app name", placeholder: "repo-wrangler" },
  ],
  cloudflare: [
    { key: "accountId", label: "Account ID", placeholder: "Cloudflare account identifier" },
    { key: "workerName", label: "Worker name", placeholder: "repo-wrangler" },
    { key: "databaseName", label: "D1 database name", placeholder: "repo-wrangler" },
    { key: "customDomain", label: "Custom domain", placeholder: "wrangler.example.com", optional: true },
  ],
  "local-compose": [
    { key: "projectName", label: "Compose project", placeholder: "repo-wrangler" },
    { key: "dataVolume", label: "Persistent Docker volume", placeholder: "repo-wrangler-data" },
    { key: "listenAddress", label: "Listen address", placeholder: "127.0.0.1:8080" },
  ],
  "remote-linux-compose": [
    { key: "host", label: "Linux host", placeholder: "server.example.com" },
    { key: "port", label: "SSH port", placeholder: "22" },
    { key: "user", label: "SSH user", placeholder: "repo-wrangler" },
    { key: "installDirectory", label: "Install directory", placeholder: "/opt/repo-wrangler" },
    { key: "projectName", label: "Compose project", placeholder: "repo-wrangler" },
    { key: "hostKeySha256", label: "Pinned SSH host key", placeholder: "SHA256:..." },
  ],
};

const credentialFields: Record<string, { key: string; label: string; placeholder: string; file?: boolean }[]> = {
  "azure-container-apps": [{ key: "azureAccessToken", label: "Temporary Azure ARM access token", placeholder: "Held in memory only" }],
  cloudflare: [{ key: "cloudflareApiToken", label: "Scoped Cloudflare API token", placeholder: "Held in memory only" }],
  "local-compose": [],
  "remote-linux-compose": [
    { key: "sshPrivateKey", label: "SSH private key file (optional with password)", placeholder: ".pem or OpenSSH key", file: true },
    { key: "sshPrivateKeyPassphrase", label: "Private-key passphrase (optional)", placeholder: "Held in memory only" },
    { key: "sshPassword", label: "SSH password (optional with key)", placeholder: "Held in memory only" },
  ],
};

const token = window.location.hash.startsWith("#token=")
  ? decodeURIComponent(window.location.hash.slice(7))
  : "";

if (window.location.hash) {
  history.replaceState(null, "", window.location.pathname);
}

async function api<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(path, {
    ...init,
    headers: {
      Authorization: `Bearer ${token}`,
      "Content-Type": "application/json",
      ...init?.headers,
    },
  });
  if (!response.ok) {
    const failure = await response.json().catch(() => ({ error: `Request failed with HTTP ${response.status}` })) as { error?: string };
    throw new Error(failure.error || `Request failed with HTTP ${response.status}`);
  }
  return response.json() as Promise<T>;
}

function manifestURL(version: string) {
  return `https://github.com/WranglerLabs/repo-wrangler/releases/download/${encodeURIComponent(version)}/release-manifest.json`;
}

function App() {
  const [status, setStatus] = useState<Status | null>(null);
  const [error, setError] = useState("");
  const [version, setVersion] = useState("");
  const [target, setTarget] = useState("azure-container-apps");
  const [releaseError, setReleaseError] = useState("");
  const [verifying, setVerifying] = useState(false);
  const [artifact, setArtifact] = useState<VerifiedArtifact | null>(null);
  const [deploymentName, setDeploymentName] = useState("My RepoWrangler");
  const [configuration, setConfiguration] = useState<Record<string, string>>({});
  const [planResult, setPlanResult] = useState<DeploymentPlan | null>(null);
  const [planError, setPlanError] = useState("");
  const [preflight, setPreflight] = useState<PreflightReport | null>(null);
  const [dryRun, setDryRun] = useState<DryRunReport | null>(null);
  const [runtimeCredentials, setRuntimeCredentials] = useState<Record<string, string>>({});
  const [targetReport, setTargetReport] = useState<TargetReport | null>(null);
  const [targetRunning, setTargetRunning] = useState(false);
  const [credentialEpoch, setCredentialEpoch] = useState(0);
  const [stagedBundle, setStagedBundle] = useState<StagedBundle | null>(null);
  const [installConfirmed, setInstallConfirmed] = useState(false);
  const [installing, setInstalling] = useState(false);
  const [operationResult, setOperationResult] = useState<OperationResult | null>(null);
  const [operationKind, setOperationKind] = useState<"install" | "backup" | "update" | "azure-install" | "cloudflare-install" | null>(null);
  const [localAction, setLocalAction] = useState<"install" | "update">("install");
  const [fromVersion, setFromVersion] = useState("");
  const [operationAzureToken, setOperationAzureToken] = useState("");
  const [operationCloudflareToken, setOperationCloudflareToken] = useState("");

  useEffect(() => {
    if (!token) {
      setError("This Ranch Hand session is missing its one-time launch token. Close this tab and launch Ranch Hand again.");
      return;
    }
    api<Status>("/api/v1/status").then(setStatus).catch((reason: Error) => setError(reason.message));
  }, []);

  async function verifyRelease(event: React.FormEvent) {
    event.preventDefault();
    setVerifying(true);
    setReleaseError("");
    setArtifact(null);
    setPlanResult(null);
    setPreflight(null);
    setDryRun(null);
    setTargetReport(null);
    setStagedBundle(null);
    setInstallConfirmed(false);
    setOperationResult(null);
    setOperationKind(null);
    setOperationAzureToken("");
    setOperationCloudflareToken("");
    try {
      const result = await api<{ verified: true; artifact: VerifiedArtifact }>("/api/v1/releases/verify", {
        method: "POST",
        body: JSON.stringify({ version, target, manifestUrl: manifestURL(version) }),
      });
      setArtifact(result.artifact);
    } catch (reason) {
      setReleaseError(reason instanceof Error ? reason.message : "Release verification failed");
    } finally {
      setVerifying(false);
    }
  }

  async function createPlan(event: React.FormEvent) {
    event.preventDefault();
    setPlanError("");
    setPlanResult(null);
    setPreflight(null);
    setDryRun(null);
    setTargetReport(null);
    setStagedBundle(null);
    setInstallConfirmed(false);
    setOperationResult(null);
    setOperationKind(null);
    setOperationAzureToken("");
    setOperationCloudflareToken("");
    try {
      const cleaned = Object.fromEntries(Object.entries(configuration).filter(([, value]) => value.trim() !== ""));
      const result = await api<{ plan: DeploymentPlan }>("/api/v1/plans/create", {
        method: "POST",
        body: JSON.stringify({ name: deploymentName, version, target, configuration: cleaned }),
      });
      setPlanResult(result.plan);
    } catch (reason) {
      setPlanError(reason instanceof Error ? reason.message : "Plan creation failed");
    }
  }

  async function runPreflight() {
    if (!planResult) return;
    setPlanError("");
    try {
      const report = await api<PreflightReport>("/api/v1/plans/preflight", { method: "POST", body: JSON.stringify(planResult) });
      setPreflight(report);
      if (report.ready) {
        const staged = await api<{ staged: true; bundle: StagedBundle }>("/api/v1/bundles/stage", { method: "POST", body: JSON.stringify(planResult) });
        setStagedBundle(staged.bundle);
      }
      setDryRun(await api<DryRunReport>("/api/v1/plans/dry-run", { method: "POST", body: JSON.stringify(planResult) }));
    } catch (reason) {
      setPlanError(reason instanceof Error ? reason.message : "Preflight failed");
    }
  }

  async function exportPlan() {
    if (!planResult) return;
    const response = await fetch("/api/v1/plans/export", {
      method: "POST",
      headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
      body: JSON.stringify(planResult),
    });
    if (!response.ok) {
      const failure = await response.json().catch(() => ({ error: "Plan export failed" })) as { error?: string };
      setPlanError(failure.error || "Plan export failed");
      return;
    }
    const href = URL.createObjectURL(await response.blob());
    const link = document.createElement("a");
    link.href = href;
    link.download = "ranch-hand-deployment-plan.json";
    link.click();
    URL.revokeObjectURL(href);
  }

  async function runTargetPreflight(event: React.FormEvent) {
    event.preventDefault();
    if (!planResult) return;
    setTargetRunning(true);
    setPlanError("");
    setTargetReport(null);
    try {
      setTargetReport(await api<TargetReport>("/api/v1/targets/preflight", {
        method: "POST",
        body: JSON.stringify({ plan: planResult, credentials: runtimeCredentials }),
      }));
    } catch (reason) {
      setPlanError(reason instanceof Error ? reason.message : "Live target preflight failed");
    } finally {
      setRuntimeCredentials({});
      setCredentialEpoch((value) => value + 1);
      setTargetRunning(false);
    }
  }

  async function installLocal() {
    if (!planResult || !installConfirmed) return;
    setInstalling(true);
    setPlanError("");
    setOperationResult(null);
    try {
      setOperationResult(await api<OperationResult>("/api/v1/operations/run", {
        method: "POST",
        body: JSON.stringify({ kind: "install", plan: planResult, credentials: {} }),
      }));
      setOperationKind("install");
    } catch (reason) {
      setPlanError(reason instanceof Error ? reason.message : "Local installation failed");
    } finally {
      setInstalling(false);
    }
  }

  async function backupLocal() {
    if (!planResult) return;
    setInstalling(true);
    setPlanError("");
    try {
      setOperationResult(await api<OperationResult>("/api/v1/operations/run", {
        method: "POST",
        body: JSON.stringify({ kind: "backup", fromVersion: planResult.release.version, plan: planResult, credentials: {} }),
      }));
      setOperationKind("backup");
    } catch (reason) {
      setPlanError(reason instanceof Error ? reason.message : "Local backup failed");
    } finally {
      setInstalling(false);
    }
  }

  async function updateLocal() {
    if (!planResult || !installConfirmed || !/^v[0-9]+\.[0-9]+\.[0-9]+(?:[-+][A-Za-z0-9.-]+)?$/.test(fromVersion)) return;
    setInstalling(true);
    setPlanError("");
    setOperationResult(null);
    try {
      setOperationResult(await api<OperationResult>("/api/v1/operations/run", {
        method: "POST",
        body: JSON.stringify({ kind: "update", fromVersion, plan: planResult, credentials: {} }),
      }));
      setOperationKind("update");
    } catch (reason) {
      setPlanError(reason instanceof Error ? reason.message : "Local update failed");
    } finally {
      setInstalling(false);
    }
  }

  async function installAzure() {
    if (!planResult || !installConfirmed || !operationAzureToken) return;
    setInstalling(true);
    setPlanError("");
    setOperationResult(null);
    try {
      setOperationResult(await api<OperationResult>("/api/v1/operations/run", {
        method: "POST",
        body: JSON.stringify({ kind: "install", plan: planResult, credentials: { azureAccessToken: operationAzureToken } }),
      }));
      setOperationKind("azure-install");
    } catch (reason) {
      setPlanError(reason instanceof Error ? reason.message : "Azure evaluation installation failed");
    } finally {
      setOperationAzureToken("");
      setInstalling(false);
    }
  }

  async function installCloudflare() {
    if (!planResult || !installConfirmed || !operationCloudflareToken) return;
    setInstalling(true);
    setPlanError("");
    setOperationResult(null);
    try {
      setOperationResult(await api<OperationResult>("/api/v1/operations/run", {
        method: "POST",
        body: JSON.stringify({ kind: "install", plan: planResult, credentials: { cloudflareApiToken: operationCloudflareToken } }),
      }));
      setOperationKind("cloudflare-install");
    } catch (reason) {
      setPlanError(reason instanceof Error ? reason.message : "Cloudflare evaluation installation failed");
    } finally {
      setOperationCloudflareToken("");
      setInstalling(false);
    }
  }

  return (
    <main>
      <header>
        <span className="brand-mark" aria-hidden="true">RH</span>
        <div><p className="eyebrow">RepoWrangler deployment manager</p><h1>Ranch Hand</h1></div>
      </header>
      <section className="hero">
        <p className="eyebrow">Foundation build</p>
        <h2>Deploy RepoWrangler without cloning its source repository.</h2>
        <p className="lede">Ranch Hand will verify immutable releases, build a secret-free deployment plan, and guide lifecycle operations from this local Windows application.</p>
      </section>
      {error && <section className="notice error" role="alert"><strong>Session unavailable</strong><p>{error}</p></section>}
      {status && <section className="notice success"><strong>Local control service is ready</strong><dl><div><dt>Version</dt><dd>{status.version}</dd></div><div><dt>API</dt><dd>{status.apiVersion}</dd></div><div><dt>Platform</dt><dd>{status.platform}</dd></div></dl></section>}
      <section className="release-panel" aria-labelledby="release-heading">
        <p className="eyebrow">Immutable release</p>
        <h2 id="release-heading">Verify a RepoWrangler bundle</h2>
        <p>Choose an explicit release and deployment target. Ranch Hand downloads only the published bundle, verifies its declared size and SHA-256, and stores the verified bytes in its local versioned cache.</p>
        <form onSubmit={verifyRelease}>
          <label>Release version<input required pattern="v[0-9]+\.[0-9]+\.[0-9]+([+-][A-Za-z0-9.-]+)?" placeholder="v1.0.8" value={version} onChange={(event) => setVersion(event.target.value)} /></label>
          <label>Deployment target<select value={target} onChange={(event) => { setTarget(event.target.value); setArtifact(null); setPlanResult(null); setConfiguration({}); setRuntimeCredentials({}); setTargetReport(null); setStagedBundle(null); setInstallConfirmed(false); setOperationResult(null); setOperationKind(null); setOperationAzureToken(""); setOperationCloudflareToken(""); }}><option value="azure-container-apps">Azure Container Apps</option><option value="cloudflare">Cloudflare</option><option value="local-compose">Local Docker Compose</option><option value="remote-linux-compose">Remote Linux Compose</option></select></label>
          <button type="submit" disabled={verifying || !token}>{verifying ? "Verifying and caching…" : "Verify and cache release"}</button>
        </form>
        {releaseError && <div className="inline-result error" role="alert"><strong>Release rejected</strong><p>{releaseError}</p></div>}
        {artifact && <div className="inline-result success"><strong>{artifact.cacheHit ? "Verified cached artifact" : "Downloaded and verified artifact"}</strong><dl><div><dt>Release</dt><dd>{artifact.version}</dd></div><div><dt>Target</dt><dd>{artifact.target}</dd></div><div><dt>Provenance</dt><dd>{artifact.provenanceVerified ? "Verified" : "Rejected"}</dd></div><div><dt>SBOM</dt><dd>{artifact.sbomVerified ? "Verified" : "Rejected"}</dd></div><div><dt>Size</dt><dd>{artifact.size.toLocaleString()} bytes</dd></div><div><dt>SHA-256</dt><dd className="digest">{artifact.sha256}</dd></div></dl></div>}
      </section>
      {artifact && <section className="release-panel" aria-labelledby="plan-heading">
        <p className="eyebrow">Secret-free deployment plan</p>
        <h2 id="plan-heading">Describe the target environment</h2>
        <p>Only non-secret identifiers and locations belong here. Ranch Hand binds the exported plan to the exact verified manifest and artifact digests; credentials are requested only when an operation needs them.</p>
        <form className="plan-form" onSubmit={createPlan}>
          <label>Deployment name<input required maxLength={120} value={deploymentName} onChange={(event) => setDeploymentName(event.target.value)} /></label>
          {targetFields[target].map((field) => <label key={field.key}>{field.label}{field.optional ? " (optional)" : ""}<input required={!field.optional} placeholder={field.placeholder} value={configuration[field.key] || ""} onChange={(event) => setConfiguration({ ...configuration, [field.key]: event.target.value })} /></label>)}
          <button type="submit">Create bound plan</button>
        </form>
        {planError && <div className="inline-result error" role="alert"><strong>Plan operation rejected</strong><p>{planError}</p></div>}
        {planResult && <div className="inline-result success"><strong>Versioned plan created</strong><p>This plan contains no credential fields and is bound to {planResult.release.version} / {planResult.target.kind}.</p><div className="button-row"><button type="button" onClick={runPreflight}>Preflight and dry run</button><button type="button" className="secondary" onClick={exportPlan}>Export JSON plan</button></div></div>}
        {preflight && <div className={`inline-result ${preflight.ready ? "success" : "error"}`}><strong>{preflight.ready ? "Preflight ready" : "Preflight blocked"}</strong><ul>{preflight.checks.map((check) => <li key={check.name}>{check.ok ? "✓" : "✕"} {check.message}</li>)}</ul></div>}
        {stagedBundle && <div className="inline-result success"><strong>{stagedBundle.cacheHit ? "Verified staged bundle reused" : "Verified bundle staged"}</strong><p>Every extracted file is recorded by size and SHA-256 and will be rechecked before reuse.</p></div>}
        {dryRun && <div className="inline-result success"><strong>Dry run completed without changes</strong><ol>{dryRun.steps.map((step) => <li key={step.order}>{step.description}</li>)}</ol></div>}
        {dryRun && <form key={`${target}-${credentialEpoch}`} className="credential-form" onSubmit={runTargetPreflight}>
          <div className="form-intro"><strong>Live target preflight</strong><p>Ranch Hand connects through the platform's native API. Credentials are sent only to this loopback process, are excluded from the plan, and are cleared from the form after the check.</p></div>
          {credentialFields[target].map((field) => <label key={field.key}>{field.label}{field.file ? <input type="file" accept=".pem,.key" onChange={async (event) => { const file = event.target.files?.[0]; if (file && file.size > 1024 * 1024) { setPlanError("SSH private key file exceeds the 1 MiB safety limit"); return; } const contents = file ? await file.text() : ""; setRuntimeCredentials((current) => ({ ...current, [field.key]: contents })); }} /> : <input type="password" required={target !== "remote-linux-compose"} placeholder={field.placeholder} value={runtimeCredentials[field.key] || ""} onChange={(event) => setRuntimeCredentials({ ...runtimeCredentials, [field.key]: event.target.value })} />}</label>)}
          <button type="submit" disabled={targetRunning}>{targetRunning ? "Checking target…" : "Run live target preflight"}</button>
        </form>}
        {targetReport && <div className={`inline-result ${targetReport.ready ? "success" : "error"}`}><strong>{targetReport.ready ? "Target is ready" : "Target preflight blocked"}</strong><ul>{targetReport.checks.map((check) => <li key={check.name}>{check.ok ? "✓" : "✕"} {check.message}</li>)}</ul></div>}
        {target === "local-compose" && targetReport?.ready && stagedBundle && !operationResult && <div className="inline-result install-panel"><strong>Apply local evaluation plan</strong><label>Operation<select value={localAction} onChange={(event) => { setLocalAction(event.target.value as "install" | "update"); setInstallConfirmed(false); }}><option value="install">New installation</option><option value="update">Backup-first update</option></select></label>{localAction === "install" ? <><p>This installs RepoWrangler in demo mode with SQLite, binds only to 127.0.0.1, and creates no proxy or public ingress.</p><label className="confirmation"><input type="checkbox" checked={installConfirmed} onChange={(event) => setInstallConfirmed(event.target.checked)} /> I understand this is a local evaluation install.</label><button type="button" disabled={!installConfirmed || installing} onClick={installLocal}>{installing ? "Installing and verifying…" : "Install local evaluation"}</button></> : <><p>Ranch Hand will verify and back up the current owned container, seed a new volume, preserve the old container and volume for rollback, activate the immutable release selected above, and recover automatically if readiness fails.</p><label>Currently installed immutable version<input required pattern="v[0-9]+\.[0-9]+\.[0-9]+([+-][A-Za-z0-9.-]+)?" placeholder="v1.0.8" value={fromVersion} onChange={(event) => setFromVersion(event.target.value)} /></label><label className="confirmation"><input type="checkbox" checked={installConfirmed} onChange={(event) => setInstallConfirmed(event.target.checked)} /> I understand the running local instance will have brief downtime during backup and activation.</label><button type="button" disabled={!installConfirmed || !fromVersion || fromVersion === planResult?.release.version || installing} onClick={updateLocal}>{installing ? "Backing up and updating…" : "Back up and update local evaluation"}</button></>}</div>}
        {target === "azure-container-apps" && targetReport?.ready && stagedBundle && !operationResult && <div className="inline-result install-panel"><strong>Install Azure evaluation instance</strong><p>Ranch Hand will create the new dedicated resource group, deploy the verified compiled ARM template in demo/SQLite mode, and expose only Azure Container Apps managed HTTPS. Existing resource groups, custom domains, production credentials, and Azure updates are not enabled in this adapter.</p><label>Fresh Azure ARM access token<input type="password" required placeholder="Held in memory only and cleared after use" value={operationAzureToken} onChange={(event) => setOperationAzureToken(event.target.value)} /></label><label className="confirmation"><input type="checkbox" checked={installConfirmed} onChange={(event) => setInstallConfirmed(event.target.checked)} /> I understand this creates billable Azure resources in a dedicated evaluation resource group.</label><button type="button" disabled={!installConfirmed || !operationAzureToken || installing} onClick={installAzure}>{installing ? "Deploying and verifying Azure…" : "Install Azure evaluation"}</button></div>}
        {target === "cloudflare" && targetReport?.ready && stagedBundle && !operationResult && <div className="inline-result install-panel"><strong>Install Cloudflare evaluation instance</strong><p>Ranch Hand will create a new dedicated D1 database, apply the verified migrations, upload the immutable Worker and web assets through Cloudflare's native API, configure the published schedules, and expose only Cloudflare-managed workers.dev HTTPS. Existing resources, custom domains, production secrets, and Cloudflare updates are not enabled in this adapter.</p><label>Fresh scoped Cloudflare API token<input type="password" required placeholder="Held in memory only and cleared after use" value={operationCloudflareToken} onChange={(event) => setOperationCloudflareToken(event.target.value)} /></label><label className="confirmation"><input type="checkbox" checked={installConfirmed} onChange={(event) => setInstallConfirmed(event.target.checked)} /> I understand this creates Cloudflare Worker and D1 resources in evaluation mode.</label><button type="button" disabled={!installConfirmed || !operationCloudflareToken || installing} onClick={installCloudflare}>{installing ? "Deploying and verifying Cloudflare…" : "Install Cloudflare evaluation"}</button></div>}
        {operationResult && operationKind === "install" && <div className="inline-result success"><strong>Local RepoWrangler installation committed</strong><p>The container passed its readiness check and the lifecycle journal is {operationResult.operation.journal.phase}. Open <a href={`http://${planResult?.configuration.listenAddress}`} target="_blank" rel="noreferrer">http://{planResult?.configuration.listenAddress}</a>.</p><button type="button" className="secondary" disabled={installing} onClick={backupLocal}>{installing ? "Creating consistent backup…" : "Back up local data"}</button></div>}
        {operationResult && operationKind === "backup" && <div className="inline-result success"><strong>Consistent local backup committed</strong><p>Ranch Hand archived the managed container's persistent data while preserving its original running or stopped state. A running container was restarted and readiness-verified. The lifecycle journal is {operationResult.operation.journal.phase}.</p>{operationResult.operation.backup && <dl><div><dt>Archive</dt><dd>{operationResult.operation.backup.artifact.locator}</dd></div><div><dt>Size</dt><dd>{operationResult.operation.backup.artifact.size.toLocaleString()} bytes</dd></div><div><dt>SHA-256</dt><dd className="digest">{operationResult.operation.backup.artifact.sha256}</dd></div></dl>}</div>}
        {operationResult && operationKind === "update" && <div className="inline-result success"><strong>Backup-first local update committed</strong><p>The new immutable container passed readiness verification. The prior container and volume remain stopped in the rollback pool, and the lifecycle journal is {operationResult.operation.journal.phase}.</p>{operationResult.operation.backup && <dl><div><dt>Rollback archive</dt><dd>{operationResult.operation.backup.artifact.locator}</dd></div><div><dt>Size</dt><dd>{operationResult.operation.backup.artifact.size.toLocaleString()} bytes</dd></div><div><dt>SHA-256</dt><dd className="digest">{operationResult.operation.backup.artifact.sha256}</dd></div></dl>}<button type="button" className="secondary" disabled={installing} onClick={backupLocal}>{installing ? "Creating consistent backup…" : "Back up updated local data"}</button></div>}
        {operationResult && operationKind === "azure-install" && <div className="inline-result success"><strong>Azure evaluation installation committed</strong><p>The ARM deployment, digest-pinned image, Azure-managed HTTPS endpoint, readiness, and exact immutable release identity passed verification. The lifecycle journal is {operationResult.operation.journal.phase}.</p></div>}
        {operationResult && operationKind === "cloudflare-install" && <div className="inline-result success"><strong>Cloudflare evaluation installation committed</strong><p>The D1 ownership marker and migrations, Worker module and assets, schedules, Cloudflare-managed HTTPS endpoint, readiness, and exact immutable release identity passed verification. The lifecycle journal is {operationResult.operation.journal.phase}.</p></div>}
      </section>}
      <section className="grid" aria-label="Initial deployment targets">
        {[
          ["Azure Container Apps", "Azure-native HTTPS and managed runtime."],
          ["Local Docker Compose", "A workstation, mini-PC, home server, or lab host."],
          ["Remote Linux Compose", "A Linux VM or server managed securely over SSH."],
          ["Cloudflare", "The reference Worker, D1, and static web profile."],
        ].map(([name, detail]) => <article key={name}><h3>{name}</h3><p>{detail}</p><span>{name === "Remote Linux Compose" ? "Adapter planned" : "Evaluation install available"}</span></article>)}
      </section>
      <footer>Ranch Hand runs on loopback only. Deployment credentials are never stored in deployment plans.</footer>
    </main>
  );
}

createRoot(document.getElementById("root")!).render(<React.StrictMode><App /></React.StrictMode>);
