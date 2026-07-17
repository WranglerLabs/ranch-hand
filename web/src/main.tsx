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

const targetFields: Record<string, { key: string; label: string; placeholder: string; optional?: boolean }[]> = {
  "azure-container-apps": [
    { key: "subscriptionId", label: "Subscription ID", placeholder: "00000000-0000-0000-0000-000000000000" },
    { key: "resourceGroup", label: "Resource group", placeholder: "rg-repo-wrangler" },
    { key: "location", label: "Azure region", placeholder: "eastus" },
    { key: "environmentName", label: "Container Apps environment", placeholder: "cae-repo-wrangler" },
    { key: "appName", label: "Container app name", placeholder: "repo-wrangler" },
    { key: "customDomain", label: "Custom domain", placeholder: "wrangler.example.com", optional: true },
  ],
  cloudflare: [
    { key: "accountId", label: "Account ID", placeholder: "Cloudflare account identifier" },
    { key: "workerName", label: "Worker name", placeholder: "repo-wrangler" },
    { key: "databaseName", label: "D1 database name", placeholder: "repo-wrangler" },
    { key: "customDomain", label: "Custom domain", placeholder: "wrangler.example.com", optional: true },
  ],
  "local-compose": [
    { key: "projectName", label: "Compose project", placeholder: "repo-wrangler" },
    { key: "dataDirectory", label: "Data directory", placeholder: "C:\\RepoWrangler\\data" },
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
      setPreflight(await api<PreflightReport>("/api/v1/plans/preflight", { method: "POST", body: JSON.stringify(planResult) }));
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
          <label>Deployment target<select value={target} onChange={(event) => { setTarget(event.target.value); setArtifact(null); setPlanResult(null); setConfiguration({}); }}><option value="azure-container-apps">Azure Container Apps</option><option value="cloudflare">Cloudflare</option><option value="local-compose">Local Docker Compose</option><option value="remote-linux-compose">Remote Linux Compose</option></select></label>
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
        {dryRun && <div className="inline-result success"><strong>Dry run completed without changes</strong><ol>{dryRun.steps.map((step) => <li key={step.order}>{step.description}</li>)}</ol></div>}
      </section>}
      <section className="grid" aria-label="Initial deployment targets">
        {[
          ["Azure Container Apps", "Azure-native HTTPS and managed runtime."],
          ["Local Docker Compose", "A workstation, mini-PC, home server, or lab host."],
          ["Remote Linux Compose", "A Linux VM or server managed securely over SSH."],
          ["Cloudflare", "The reference Worker, D1, and static web profile."],
        ].map(([name, detail]) => <article key={name}><h3>{name}</h3><p>{detail}</p><span>Adapter planned</span></article>)}
      </section>
      <footer>Ranch Hand runs on loopback only. Deployment credentials are never stored in deployment plans.</footer>
    </main>
  );
}

createRoot(document.getElementById("root")!).render(<React.StrictMode><App /></React.StrictMode>);
