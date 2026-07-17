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
          <label>Deployment target<select value={target} onChange={(event) => setTarget(event.target.value)}><option value="azure-container-apps">Azure Container Apps</option><option value="cloudflare">Cloudflare</option><option value="local-compose">Local Docker Compose</option><option value="remote-linux-compose">Remote Linux Compose</option></select></label>
          <button type="submit" disabled={verifying || !token}>{verifying ? "Verifying and caching…" : "Verify and cache release"}</button>
        </form>
        {releaseError && <div className="inline-result error" role="alert"><strong>Release rejected</strong><p>{releaseError}</p></div>}
        {artifact && <div className="inline-result success"><strong>{artifact.cacheHit ? "Verified cached artifact" : "Downloaded and verified artifact"}</strong><dl><div><dt>Release</dt><dd>{artifact.version}</dd></div><div><dt>Target</dt><dd>{artifact.target}</dd></div><div><dt>Size</dt><dd>{artifact.size.toLocaleString()} bytes</dd></div><div><dt>SHA-256</dt><dd className="digest">{artifact.sha256}</dd></div></dl></div>}
      </section>
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
