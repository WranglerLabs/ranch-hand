import React, { useEffect, useState } from "react";
import { createRoot } from "react-dom/client";
import "./styles.css";

type Status = {
  name: string;
  version: string;
  apiVersion: string;
  platform: string;
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
  if (!response.ok) throw new Error(await response.text());
  return response.json() as Promise<T>;
}

function App() {
  const [status, setStatus] = useState<Status | null>(null);
  const [error, setError] = useState("");

  useEffect(() => {
    if (!token) {
      setError("This Ranch Hand session is missing its one-time launch token. Close this tab and launch Ranch Hand again.");
      return;
    }
    api<Status>("/api/v1/status").then(setStatus).catch((reason: Error) => setError(reason.message));
  }, []);

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
