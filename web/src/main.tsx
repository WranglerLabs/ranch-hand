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
type TargetReport = { ready: boolean; target: string; state?: "recovery-required" | "already-installed" | "lifecycle-unavailable"; deploymentId?: string; checks: { name: string; ok: boolean; message: string }[] };
type StagedBundle = { product: string; version: string; target: string; path: string; cacheHit: boolean };
type OperationResult = { completed: boolean; operation: { journal: { phase: string }; backup?: { artifact: { locator: string; size: number; sha256: string } } } };
type InstallationRecord = { deploymentId: string; target: string; state: "active" | "uninstalled"; version: string; plan: DeploymentPlan; updatedAt: string };
type BackupRecord = { backupId: string; deploymentId: string; target: string; version: string; createdAt: string; artifact: { locator: string; size: number; sha256: string } };
type ActiveOperation = { deploymentId: string; operationId: string; kind: string; target: string; fromVersion?: string; toVersion: string; phase: string; updatedAt: string };
type RollbackPoolEntry = { backupId: string; version: string; createdAt: string; containerName: string; volumeName: string };
type DiscoveredRelease = { version: string; manifestUrl: string; prerelease: boolean };

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
  "local-wsl-compose": [
    { key: "distribution", label: "WSL distribution", placeholder: "Detected automatically" },
    { key: "projectName", label: "Compose project", placeholder: "repo-wrangler-ranch-hand" },
  ],
  "remote-linux-compose": [
    { key: "host", label: "Linux host", placeholder: "server.example.com" },
    { key: "port", label: "SSH port", placeholder: "22" },
    { key: "user", label: "SSH user", placeholder: "ubuntu" },
    { key: "installDirectory", label: "Install directory", placeholder: "Filled from the SSH user" },
    { key: "projectName", label: "Compose project", placeholder: "repo-wrangler-ranch-hand" },
    { key: "hostKeySha256", label: "Pinned SSH host key", placeholder: "SHA256:..." },
  ],
};

const credentialFields: Record<string, { key: string; label: string; placeholder: string; file?: boolean }[]> = {
  "azure-container-apps": [{ key: "azureAccessToken", label: "Temporary Azure ARM access token", placeholder: "Held in memory only" }],
  cloudflare: [{ key: "cloudflareApiToken", label: "Scoped Cloudflare API token", placeholder: "Held in memory only" }],
  "local-compose": [],
  "local-wsl-compose": [],
  "remote-linux-compose": [
    { key: "sshPrivateKey", label: "SSH private key file (optional with password)", placeholder: ".pem or OpenSSH key", file: true },
    { key: "sshPrivateKeyPassphrase", label: "Private-key passphrase (optional)", placeholder: "Held in memory only" },
    { key: "sshPassword", label: "SSH password (optional with key)", placeholder: "Held in memory only" },
  ],
};

const targetDefaults: Record<string, Record<string, string>> = {
  "local-compose": { projectName: "repo-wrangler", dataVolume: "repo-wrangler-data", listenAddress: "127.0.0.1:8080" },
  "local-wsl-compose": { projectName: "repo-wrangler-ranch-hand" },
  "remote-linux-compose": { port: "22", projectName: "repo-wrangler-ranch-hand" },
};

function remoteInstallDirectory(user: string): string {
  if (!user) return "";
  return user === "root" ? "/root/.repo-wrangler-ranch-hand" : `/home/${user}/.repo-wrangler-ranch-hand`;
}

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
  const [releaseChoice, setReleaseChoice] = useState<"stable" | "prerelease" | "specific">("stable");
  const [releaseLoading, setReleaseLoading] = useState(false);
  const [target, setTarget] = useState("azure-container-apps");
  const [releaseError, setReleaseError] = useState("");
  const [verifying, setVerifying] = useState(false);
  const [artifact, setArtifact] = useState<VerifiedArtifact | null>(null);
  const [deploymentName, setDeploymentName] = useState("My RepoWrangler");
  const [configuration, setConfiguration] = useState<Record<string, string>>({});
  const [wslDistributions, setWSLDistributions] = useState<string[]>([]);
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
  const [wslInstallMessage, setWSLInstallMessage] = useState("");
  const [operationResult, setOperationResult] = useState<OperationResult | null>(null);
  const [operationKind, setOperationKind] = useState<"install" | "wsl-install" | "backup" | "update" | "restore" | "rollback" | "repair" | "azure-install" | "cloudflare-install" | "remote-install" | null>(null);
  const [localAction, setLocalAction] = useState<"install" | "update">("install");
  const [fromVersion, setFromVersion] = useState("");
  const [currentInstallation, setCurrentInstallation] = useState<InstallationRecord | null>(null);
  const [installations, setInstallations] = useState<InstallationRecord[]>([]);
  const [backups, setBackups] = useState<BackupRecord[]>([]);
  const [selectedBackupId, setSelectedBackupId] = useState("");
  const [recoveryConfirmed, setRecoveryConfirmed] = useState(false);
  const [inventoryLoading, setInventoryLoading] = useState(false);
  const [operationAzureToken, setOperationAzureToken] = useState("");
  const [operationCloudflareToken, setOperationCloudflareToken] = useState("");
  const [operationSSHCredentials, setOperationSSHCredentials] = useState<Record<string, string>>({});
  const [activeOperations, setActiveOperations] = useState<ActiveOperation[]>([]);
  const [recoveryCredentials, setRecoveryCredentials] = useState<Record<string, Record<string, string>>>({});
  const [recoveringDeployment, setRecoveringDeployment] = useState("");
  const [recoveryMessage, setRecoveryMessage] = useState("");
  const [rollbackPool, setRollbackPool] = useState<RollbackPoolEntry[]>([]);
  const [rollbackKeepLatest, setRollbackKeepLatest] = useState(1);
  const [rollbackPruneConfirmed, setRollbackPruneConfirmed] = useState(false);
  const [rollbackPruning, setRollbackPruning] = useState(false);

  function changeConfiguration(next: Record<string, string>) {
    setConfiguration(next);
    setPlanResult(null);
    setPreflight(null);
    setDryRun(null);
    setTargetReport(null);
    setStagedBundle(null);
    setInstallConfirmed(false);
    setWSLInstallMessage("");
    setOperationResult(null);
    setOperationKind(null);
  }

  useEffect(() => {
    if (!token) {
      setError("This Ranch Hand session is missing its one-time launch token. Close this tab and launch Ranch Hand again.");
      return;
    }
    api<Status>("/api/v1/status").then(setStatus).catch((reason: Error) => setError(reason.message));
    refreshActiveOperations();
    refreshInstallations();
  }, []);

  useEffect(() => {
    if (!token || releaseChoice === "specific") return;
    let cancelled = false;
    setReleaseLoading(true);
    setReleaseError("");
    setVersion("");
    api<{ release: DiscoveredRelease }>(`/api/v1/releases/recommended?channel=${releaseChoice}&target=${encodeURIComponent(target)}`)
      .then(({ release }) => { if (!cancelled) setVersion(release.version); })
      .catch((reason: Error) => { if (!cancelled) setReleaseError(reason.message); })
      .finally(() => { if (!cancelled) setReleaseLoading(false); });
    return () => { cancelled = true; };
  }, [releaseChoice, target]);

  useEffect(() => {
    if (target !== "local-wsl-compose" || !token) return;
    api<{ distributions: string[] }>("/api/v1/targets/wsl-distributions")
      .then(({ distributions }) => {
        setWSLDistributions(distributions);
        setConfiguration((current) => ({ ...targetDefaults["local-wsl-compose"], ...current, distribution: current.distribution || distributions[0] || "" }));
      })
      .catch((reason: Error) => setPlanError(reason.message));
  }, [target]);

  async function refreshActiveOperations() {
    try {
      const result = await api<{ operations: ActiveOperation[] }>("/api/v1/operations/active");
      setActiveOperations(result.operations);
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : "Active operation inventory failed");
    }
  }

  async function refreshInstallations() {
    try {
      const result = await api<{ installations: InstallationRecord[] }>("/api/v1/installations");
      setInstallations(result.installations);
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : "Installation inventory failed");
    }
  }

  useEffect(() => {
    if (!installing) return;
    void refreshActiveOperations();
    const timer = window.setInterval(() => { void refreshActiveOperations(); }, 1000);
    return () => window.clearInterval(timer);
  }, [installing]);

  useEffect(() => {
    if (operationResult) void refreshInstallations();
  }, [operationResult]);

  async function recoverActiveOperation(operation: ActiveOperation) {
    setRecoveringDeployment(operation.deploymentId);
    setRecoveryMessage("");
    try {
      const result = await api<{ completed: boolean; operation: { recovered: boolean; safelyClosed: boolean } }>(`/api/v1/operations/${operation.deploymentId}/recover`, {
        method: "POST",
        body: JSON.stringify({ credentials: recoveryCredentials[operation.deploymentId] || {} }),
      });
      setRecoveryMessage(result.operation.safelyClosed ? "The pre-apply operation was safely closed." : "Target recovery completed and the operation lock was released.");
      setRecoveryCredentials((current) => ({ ...current, [operation.deploymentId]: {} }));
      await refreshActiveOperations();
      await refreshInstallations();
      if (targetReport?.deploymentId === operation.deploymentId) {
        setTargetReport(null);
        setRecoveryMessage(result.operation.safelyClosed ? "The pre-apply operation was safely closed. Run live target preflight again." : "Target recovery completed and the operation lock was released. Run live target preflight again.");
      }
    } catch (reason) {
      setRecoveryMessage(reason instanceof Error ? reason.message : "Active operation recovery failed");
    } finally {
      setRecoveringDeployment("");
    }
  }

  function recoveryCredentialsReady(operation: ActiveOperation, values: Record<string, string>) {
    if (operation.phase === "prepared" || operation.phase === "backup-complete" || operation.target === "local-compose" || operation.target === "local-wsl-compose") return true;
    if (operation.target === "azure-container-apps") return Boolean(values.azureAccessToken?.trim());
    if (operation.target === "cloudflare") return Boolean(values.cloudflareApiToken?.trim());
    if (operation.target === "remote-linux-compose") return Boolean(values.sshPrivateKey?.trim() || values.sshPassword?.trim());
    return false;
  }

  useEffect(() => {
    if (!planResult || planResult.target.kind !== "local-compose" || !targetReport?.ready) return;
    let cancelled = false;
    setInventoryLoading(true);
    api<{ installations: InstallationRecord[] }>("/api/v1/installations")
      .then(async ({ installations }) => {
        const configurationKey = (value: Record<string, string>) => JSON.stringify(Object.entries(value).sort(([left], [right]) => left.localeCompare(right)));
        const current = installations.find((record) => record.state === "active" && record.target === "local-compose" && configurationKey(record.plan.configuration) === configurationKey(planResult.configuration)) || null;
        if (cancelled) return;
        setCurrentInstallation(current);
        setFromVersion(current?.version || "");
        setLocalAction(current ? "update" : "install");
        if (!current) {
          setBackups([]);
          setRollbackPool([]);
          setSelectedBackupId("");
          setRecoveryConfirmed(false);
          return;
        }
        const [inventory, pool] = await Promise.all([
          api<{ backups: BackupRecord[] }>(`/api/v1/installations/${current.deploymentId}/backups`),
          api<{ entries: RollbackPoolEntry[] }>(`/api/v1/installations/${current.deploymentId}/rollback-pool`),
        ]);
        if (!cancelled) {
          setBackups(inventory.backups);
          setRollbackPool(pool.entries);
          setRollbackKeepLatest(Math.min(1, pool.entries.length));
          setRollbackPruneConfirmed(false);
          setSelectedBackupId("");
          setRecoveryConfirmed(false);
        }
      })
      .catch((reason: Error) => { if (!cancelled) setPlanError(reason.message); })
      .finally(() => { if (!cancelled) setInventoryLoading(false); });
    return () => { cancelled = true; };
  }, [planResult, targetReport?.ready]);

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
    setOperationSSHCredentials({});
    setCurrentInstallation(null);
    setBackups([]);
    setSelectedBackupId("");
    setRecoveryConfirmed(false);
    setRollbackPool([]);
    setRollbackPruneConfirmed(false);
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
    setOperationSSHCredentials({});
    setCurrentInstallation(null);
    setBackups([]);
    setSelectedBackupId("");
    setRecoveryConfirmed(false);
    setRollbackPool([]);
    setRollbackPruneConfirmed(false);
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

  async function exportDiagnostics() {
    const response = await fetch("/api/v1/diagnostics", { headers: { Authorization: `Bearer ${token}` } });
    if (!response.ok) {
      const failure = await response.json().catch(() => ({ error: "Diagnostics export failed" })) as { error?: string };
      setError(failure.error || "Diagnostics export failed");
      return;
    }
    const href = URL.createObjectURL(await response.blob());
    const link = document.createElement("a");
    link.href = href;
    link.download = "ranch-hand-diagnostics.json";
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
      const report = await api<TargetReport>("/api/v1/targets/preflight", {
        method: "POST",
        body: JSON.stringify({ plan: planResult, credentials: runtimeCredentials }),
      });
      setTargetReport(report);
      if (report.state === "recovery-required") await refreshActiveOperations();
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

  async function installWSL() {
    if (!planResult) {
      setWSLInstallMessage("Create and preflight the WSL deployment plan before installing.");
      return;
    }
    if (!installConfirmed) {
      setWSLInstallMessage("Select the confirmation checkbox before starting the WSL installation.");
      return;
    }
    setInstalling(true);
    setOperationKind("wsl-install");
    setWSLInstallMessage("Submitting the WSL installation. Docker may need several minutes to pull and start the verified image.");
    setPlanError("");
    setOperationResult(null);
    try {
      setOperationResult(await api<OperationResult>("/api/v1/operations/run", {
        method: "POST",
        body: JSON.stringify({ kind: "install", plan: planResult, credentials: {} }),
      }));
      setWSLInstallMessage("");
    } catch (reason) {
      const message = reason instanceof Error ? reason.message : "Local WSL Compose installation failed";
      setPlanError(message);
      setWSLInstallMessage(message);
    } finally {
      await refreshActiveOperations();
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

  async function restoreOrRollbackLocal(kind: "restore" | "rollback") {
    if (!planResult || !currentInstallation || !selectedBackupId || !recoveryConfirmed) return;
    setInstalling(true);
    setPlanError("");
    setOperationResult(null);
    try {
      setOperationResult(await api<OperationResult>("/api/v1/operations/run", {
        method: "POST",
        body: JSON.stringify({ kind, fromVersion: currentInstallation.version, backupId: selectedBackupId, plan: planResult, credentials: {} }),
      }));
      setOperationKind(kind);
    } catch (reason) {
      setPlanError(reason instanceof Error ? reason.message : `Local ${kind} failed`);
    } finally {
      setInstalling(false);
    }
  }

  async function repairLocal() {
    if (!planResult || !currentInstallation || !recoveryConfirmed || planResult.release.version !== currentInstallation.version) return;
    setInstalling(true);
    setPlanError("");
    setOperationResult(null);
    try {
      setOperationResult(await api<OperationResult>("/api/v1/operations/run", {
        method: "POST",
        body: JSON.stringify({ kind: "repair", fromVersion: currentInstallation.version, plan: planResult, credentials: {} }),
      }));
      setOperationKind("repair");
    } catch (reason) {
      setPlanError(reason instanceof Error ? reason.message : "Local repair failed");
    } finally {
      setInstalling(false);
    }
  }

  async function pruneRollbackPool() {
    if (!currentInstallation || !rollbackPruneConfirmed) return;
    setRollbackPruning(true);
    setPlanError("");
    try {
      await api(`/api/v1/installations/${currentInstallation.deploymentId}/rollback-pool/prune`, {
        method: "POST",
        body: JSON.stringify({ keepLatest: rollbackKeepLatest, confirmed: true }),
      });
      const pool = await api<{ entries: RollbackPoolEntry[] }>(`/api/v1/installations/${currentInstallation.deploymentId}/rollback-pool`);
      setRollbackPool(pool.entries);
      setRollbackKeepLatest(Math.min(rollbackKeepLatest, pool.entries.length));
      setRollbackPruneConfirmed(false);
    } catch (reason) {
      setPlanError(reason instanceof Error ? reason.message : "Rollback-pool pruning failed");
    } finally {
      setRollbackPruning(false);
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

  async function installRemote() {
    if (!planResult || !installConfirmed || (!operationSSHCredentials.sshPrivateKey && !operationSSHCredentials.sshPassword)) return;
    setInstalling(true);
    setPlanError("");
    setOperationResult(null);
    try {
      setOperationResult(await api<OperationResult>("/api/v1/operations/run", {
        method: "POST",
        body: JSON.stringify({ kind: "install", plan: planResult, credentials: operationSSHCredentials }),
      }));
      setOperationKind("remote-install");
    } catch (reason) {
      setPlanError(reason instanceof Error ? reason.message : "Remote Linux evaluation installation failed");
    } finally {
      setOperationSSHCredentials({});
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
      {status && <section className="notice success"><strong>Local control service is ready</strong><dl><div><dt>Version</dt><dd>{status.version}</dd></div><div><dt>API</dt><dd>{status.apiVersion}</dd></div><div><dt>Platform</dt><dd>{status.platform}</dd></div></dl><button type="button" className="secondary" onClick={exportDiagnostics}>Export redacted diagnostics</button></section>}
      {recoveryMessage && <section className="notice"><strong>Lifecycle recovery</strong><p>{recoveryMessage}</p></section>}
      {installations.some((record) => record.state === "active") && <section className="release-panel" aria-labelledby="deployments-heading"><p className="eyebrow">Lifecycle inventory</p><h2 id="deployments-heading">Managed deployments</h2><p>These durable records are Ranch Hand's authoritative deployment inventory. The launcher terminal is only a startup and diagnostic surface.</p>{installations.filter((record) => record.state === "active").map((record) => <div className="inline-result success" key={record.deploymentId}><strong>{record.plan.name}</strong><dl><div><dt>Target</dt><dd>{record.target}</dd></div><div><dt>Project</dt><dd>{record.plan.configuration.projectName || record.plan.configuration.appName || record.plan.configuration.workerName || "Managed target"}</dd></div><div><dt>Version</dt><dd>{record.version}</dd></div><div><dt>State</dt><dd>{record.state}</dd></div><div><dt>Updated</dt><dd>{new Date(record.updatedAt).toLocaleString()}</dd></div></dl>{record.target === "local-wsl-compose" && <a href="http://127.0.0.1:8080" target="_blank" rel="noreferrer">Open RepoWrangler</a>}</div>)}</section>}
      {activeOperations.length > 0 && <section className="release-panel" aria-labelledby="recovery-heading"><p className="eyebrow">Interrupted lifecycle work</p><h2 id="recovery-heading">Recover active operations</h2><p>Ranch Hand found durable operation locks from an interrupted session. Pre-apply phases can be closed without touching the target. A phase where apply may have started reruns the adapter's ownership-checked recovery with fresh in-memory credentials.</p>{activeOperations.map((operation) => { const preApply = operation.phase === "prepared" || operation.phase === "backup-complete"; const fields = preApply || operation.target === "local-compose" ? [] : (credentialFields[operation.target] || []); const values = recoveryCredentials[operation.deploymentId] || {}; const credentialsReady = recoveryCredentialsReady(operation, values); return <div className="inline-result install-panel" key={operation.operationId}><strong>{operation.kind} — {operation.target}</strong><p>Phase: {operation.phase}. Release: {operation.fromVersion ? `${operation.fromVersion} → ` : ""}{operation.toVersion}. Last journal update: {new Date(operation.updatedAt).toLocaleString()}.</p>{fields.map((field) => <label key={field.key}>{field.label}{field.file ? <input type="file" accept=".pem,.key" onChange={async (event) => { const file = event.target.files?.[0]; if (file && file.size > 1024 * 1024) { setRecoveryMessage("SSH private key file exceeds the 1 MiB safety limit"); return; } const contents = file ? await file.text() : ""; setRecoveryCredentials((current) => ({ ...current, [operation.deploymentId]: { ...(current[operation.deploymentId] || {}), [field.key]: contents } })); }} /> : <input type="password" placeholder={field.placeholder} value={values[field.key] || ""} onChange={(event) => setRecoveryCredentials((current) => ({ ...current, [operation.deploymentId]: { ...(current[operation.deploymentId] || {}), [field.key]: event.target.value } }))} />}</label>)}<button type="button" disabled={recoveringDeployment !== "" || !credentialsReady} onClick={() => recoverActiveOperation(operation)}>{recoveringDeployment === operation.deploymentId ? "Recovering…" : preApply ? "Safely close pre-apply operation" : operation.target === "local-wsl-compose" ? "Remove failed installation and release lock" : "Run ownership-checked recovery"}</button></div>; })}</section>}
      <section className="release-panel" aria-labelledby="release-heading">
        <p className="eyebrow">Immutable release</p>
        <h2 id="release-heading">Verify a RepoWrangler bundle</h2>
        <p>Ranch Hand selects the latest stable RepoWrangler release by default. Choose a prerelease or a specific immutable version only when you intentionally need one. Ranch Hand then verifies the published bundle before use.</p>
        <form onSubmit={verifyRelease}>
          <label>Release choice<select value={releaseChoice} onChange={(event) => { setReleaseChoice(event.target.value as "stable" | "prerelease" | "specific"); setArtifact(null); setPlanResult(null); }}><option value="stable">Latest stable (recommended)</option><option value="prerelease">Latest prerelease</option><option value="specific">Specific version (advanced)</option></select></label>
          <label>RepoWrangler version<input required readOnly={releaseChoice !== "specific"} pattern="v[0-9]+\.[0-9]+\.[0-9]+([+-][A-Za-z0-9.-]+)?" placeholder={releaseLoading ? "Finding the latest compatible release…" : "v1.0.10"} value={version} onChange={(event) => setVersion(event.target.value)} /></label>
          <label>Deployment target<select value={target} onChange={(event) => { const next = event.target.value; setTarget(next); setArtifact(null); setPlanResult(null); setConfiguration(targetDefaults[next] || {}); setRuntimeCredentials({}); setTargetReport(null); setStagedBundle(null); setInstallConfirmed(false); setWSLInstallMessage(""); setOperationResult(null); setOperationKind(null); setOperationAzureToken(""); setOperationCloudflareToken(""); setOperationSSHCredentials({}); }}><option value="local-wsl-compose">Local Docker Compose — WSL</option><option value="local-compose">Local Docker Desktop</option><option value="remote-linux-compose">Remote Linux Docker Compose</option><option value="cloudflare">Cloudflare</option><option value="azure-container-apps">Azure Container Apps</option></select></label>
          <button type="submit" disabled={verifying || releaseLoading || !version || !token}>{releaseLoading ? "Finding release…" : verifying ? "Verifying and caching…" : "Verify and cache release"}</button>
        </form>
        {releaseError && <div className="inline-result error" role="alert"><strong>Release rejected</strong><p>{releaseError}</p></div>}
        {artifact && <div className="inline-result success"><strong>{artifact.cacheHit ? "Verified cached artifact" : "Downloaded and verified artifact"}</strong><dl><div><dt>Release</dt><dd>{artifact.version}</dd></div><div><dt>Target</dt><dd>{artifact.target}</dd></div><div><dt>Provenance</dt><dd>{artifact.provenanceVerified ? "Verified" : "Rejected"}</dd></div><div><dt>SBOM</dt><dd>{artifact.sbomVerified ? "Verified" : "Rejected"}</dd></div><div><dt>Size</dt><dd>{artifact.size.toLocaleString()} bytes</dd></div><div><dt>SHA-256</dt><dd className="digest">{artifact.sha256}</dd></div></dl></div>}
      </section>
      {artifact && <section className="release-panel" aria-labelledby="plan-heading">
        <p className="eyebrow">Secret-free deployment plan</p>
        <h2 id="plan-heading">Describe the target environment</h2>
        <p>Only non-secret identifiers and locations belong here. Ranch Hand binds the exported plan to the exact verified manifest and artifact digests; credentials are requested only when an operation needs them.</p>
        <form className="plan-form" onSubmit={createPlan}>
          <label>Deployment name<input required maxLength={120} value={deploymentName} onChange={(event) => { setDeploymentName(event.target.value); changeConfiguration(configuration); }} /></label>
          {targetFields[target].map((field) => <label key={field.key}>{field.label}{field.optional ? " (optional)" : ""}{field.key === "distribution" ? <select required value={configuration.distribution || ""} onChange={(event) => changeConfiguration({ ...configuration, distribution: event.target.value })}><option value="">Select an installed WSL distribution</option>{wslDistributions.map((distribution) => <option key={distribution} value={distribution}>{distribution}</option>)}</select> : <input required={!field.optional} placeholder={field.placeholder} value={configuration[field.key] || ""} onChange={(event) => {
            const value = event.target.value;
            if (target === "remote-linux-compose" && field.key === "user") {
              const previousDefault = remoteInstallDirectory(configuration.user || "");
              const next: Record<string, string> = { ...configuration, user: value };
              if (!configuration.installDirectory || configuration.installDirectory === previousDefault) next.installDirectory = remoteInstallDirectory(value);
              changeConfiguration(next);
              return;
            }
            changeConfiguration({ ...configuration, [field.key]: value });
          }} />}</label>)}
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
        {targetReport && <div className={`inline-result ${targetReport.ready ? "success" : "error"}`}><strong>{targetReport.ready ? "Target is ready" : "Target preflight blocked"}</strong><ul>{targetReport.checks.map((check) => <li key={check.name}>{check.ok ? "✓" : "✕"} {check.message}</li>)}</ul>{targetReport.state === "recovery-required" && (() => { const operation = activeOperations.find((item) => item.deploymentId === targetReport.deploymentId); return operation ? <><button type="button" disabled={recoveringDeployment !== ""} onClick={() => recoverActiveOperation(operation)}>{recoveringDeployment === operation.deploymentId ? "Removing failed installation…" : target === "local-wsl-compose" ? "Remove failed installation and release lock" : "Recover interrupted Ranch Hand operation"}</button>{recoveryMessage && <p role="status" aria-live="polite">{recoveryMessage}</p>}</> : <p>Reload Ranch Hand to refresh the interrupted-operation record.</p>; })()}{targetReport.state === "already-installed" && target === "local-wsl-compose" && <p><a href="http://127.0.0.1:8080" target="_blank" rel="noreferrer">Open the existing RepoWrangler installation</a>. Ranch Hand will not reinstall over it.</p>}</div>}
        {target === "local-wsl-compose" && targetReport?.ready && stagedBundle && !operationResult && <div className="inline-result install-panel"><strong>Install with Docker Compose inside WSL</strong><p>Ranch Hand will transfer the verified Compose bundle into {planResult?.configuration.distribution}, use the Docker-managed volume <code>{planResult?.configuration.projectName}-data</code>, and expose RepoWrangler at <code>http://127.0.0.1:8080</code>. Docker Desktop, SSH, a WSL IP, and a filesystem path are not used.</p><label className="confirmation"><input type="checkbox" checked={installConfirmed} onChange={(event) => { setInstallConfirmed(event.target.checked); setWSLInstallMessage(""); }} /> I understand this creates a local evaluation project inside the selected WSL distribution.</label><button type="button" disabled={installing} onClick={installWSL}>{installing ? "Running Docker Compose in WSL…" : "Install in WSL"}</button>{wslInstallMessage && <p className={installing ? "operation-progress" : "operation-warning"} role={installing ? "status" : "alert"} aria-live="polite">{wslInstallMessage}</p>}{installing && <p className="operation-progress" role="status" aria-live="polite">{(() => { const operation = activeOperations.find((item) => item.deploymentId === targetReport.deploymentId); return operation ? `Current lifecycle phase: ${operation.phase}. Ranch Hand is still working; keep this window open.` : "Preparing the durable operation journal…"; })()}</p>}</div>}
        {target === "local-compose" && targetReport?.ready && stagedBundle && !operationResult && <div className="inline-result install-panel"><strong>Apply Docker Desktop evaluation plan</strong><label>Operation<select value={localAction} onChange={(event) => { setLocalAction(event.target.value as "install" | "update"); setInstallConfirmed(false); }}><option value="install">New installation</option><option value="update">Backup-first update</option></select></label>{localAction === "install" ? <><p>This uses Docker Desktop's Windows-exposed Docker Engine API, installs RepoWrangler in demo mode with SQLite, and binds only to 127.0.0.1.</p><label className="confirmation"><input type="checkbox" checked={installConfirmed} onChange={(event) => setInstallConfirmed(event.target.checked)} /> I understand this target requires Docker Desktop or an equivalent Windows-exposed Docker Engine.</label><button type="button" disabled={!installConfirmed || installing || inventoryLoading || currentInstallation !== null} onClick={installLocal}>{installing ? "Installing and verifying…" : inventoryLoading ? "Checking installation inventory…" : "Install with Docker Desktop"}</button></> : <><p>Ranch Hand will verify and back up the current owned container, seed a new volume, preserve the old container and volume for rollback, activate the immutable release selected above, and recover automatically if readiness fails.</p><label>Recorded currently installed immutable version<input readOnly value={fromVersion} placeholder={inventoryLoading ? "Loading installation record…" : "No active installation record"} /></label><label className="confirmation"><input type="checkbox" checked={installConfirmed} onChange={(event) => setInstallConfirmed(event.target.checked)} /> I understand the running local instance will have brief downtime during backup and activation.</label><button type="button" disabled={!installConfirmed || !fromVersion || fromVersion === planResult?.release.version || installing} onClick={updateLocal}>{installing ? "Backing up and updating…" : "Back up and update local evaluation"}</button></>}</div>}
        {target === "local-compose" && targetReport?.ready && stagedBundle && currentInstallation && !operationResult && <div className="inline-result install-panel"><strong>Restore, roll back, or repair recorded local data</strong><p>Recorded installation: {currentInstallation.version}. Select a verified backup for the release bound to this plan ({planResult?.release.version}). Ranch Hand first creates a fresh safety backup, writes only to a new owned volume, preserves the original container, and recovers it automatically if verification fails.</p>{inventoryLoading ? <p>Loading lifecycle inventory…</p> : <><label>Recorded backup<select value={selectedBackupId} onChange={(event) => { setSelectedBackupId(event.target.value); setRecoveryConfirmed(false); }}><option value="">Select a backup</option>{backups.filter((backup) => backup.version === planResult?.release.version).map((backup) => <option key={backup.backupId} value={backup.backupId}>{backup.version} — {new Date(backup.createdAt).toLocaleString()} — {backup.backupId.slice(0, 12)}</option>)}</select></label>{backups.every((backup) => backup.version !== planResult?.release.version) && <p>No recorded backup matches this verified release. Verify the immutable release represented by the backup you want to use, or repair the currently recorded release from a fresh safety backup.</p>}<label className="confirmation"><input type="checkbox" checked={recoveryConfirmed} onChange={(event) => setRecoveryConfirmed(event.target.checked)} /> I understand the current instance will have brief downtime and a new safety backup will be created first.</label><div className="button-row"><button type="button" disabled={!selectedBackupId || !recoveryConfirmed || installing} onClick={() => restoreOrRollbackLocal(planResult?.release.version === currentInstallation.version ? "restore" : "rollback")}>{installing ? "Protecting current state and applying backup…" : planResult?.release.version === currentInstallation.version ? "Back up current state and restore" : "Back up current state and roll back"}</button><button type="button" className="secondary" disabled={!recoveryConfirmed || installing || planResult?.release.version !== currentInstallation.version} onClick={repairLocal}>{installing ? "Repairing…" : "Back up and repair current release"}</button></div></>}</div>}
        {target === "local-compose" && currentInstallation && rollbackPool.length > 0 && !operationResult && <div className="inline-result install-panel"><strong>Rollback-pool retention</strong><p>{rollbackPool.length} stopped, ownership-verified rollback {rollbackPool.length === 1 ? "environment is" : "environments are"} consuming Docker container and volume storage. Verified backup archives and records are retained when these Docker resources are pruned.</p><label>Keep newest rollback environments<select value={rollbackKeepLatest} onChange={(event) => { setRollbackKeepLatest(Number(event.target.value)); setRollbackPruneConfirmed(false); }}>{Array.from({ length: Math.min(10, rollbackPool.length) + 1 }, (_, value) => <option key={value} value={value}>{value}</option>)}</select></label><label className="confirmation"><input type="checkbox" checked={rollbackPruneConfirmed} onChange={(event) => setRollbackPruneConfirmed(event.target.checked)} /> I understand Ranch Hand will permanently remove {Math.max(0, rollbackPool.length - rollbackKeepLatest)} older stopped rollback container and data volume {Math.max(0, rollbackPool.length - rollbackKeepLatest) === 1 ? "pair" : "pairs"} after re-verifying ownership.</label><button type="button" className="secondary" disabled={!rollbackPruneConfirmed || rollbackPruning || rollbackKeepLatest >= rollbackPool.length} onClick={pruneRollbackPool}>{rollbackPruning ? "Re-verifying and pruning…" : "Prune older rollback environments"}</button></div>}
        {target === "azure-container-apps" && targetReport?.ready && stagedBundle && !operationResult && <div className="inline-result install-panel"><strong>Install Azure evaluation instance</strong><p>Ranch Hand will create the new dedicated resource group, deploy the verified compiled ARM template in demo/SQLite mode, and expose only Azure Container Apps managed HTTPS. Existing resource groups, custom domains, production credentials, and Azure updates are not enabled in this adapter.</p><label>Fresh Azure ARM access token<input type="password" required placeholder="Held in memory only and cleared after use" value={operationAzureToken} onChange={(event) => setOperationAzureToken(event.target.value)} /></label><label className="confirmation"><input type="checkbox" checked={installConfirmed} onChange={(event) => setInstallConfirmed(event.target.checked)} /> I understand this creates billable Azure resources in a dedicated evaluation resource group.</label><button type="button" disabled={!installConfirmed || !operationAzureToken || installing} onClick={installAzure}>{installing ? "Deploying and verifying Azure…" : "Install Azure evaluation"}</button></div>}
        {target === "cloudflare" && targetReport?.ready && stagedBundle && !operationResult && <div className="inline-result install-panel"><strong>Install Cloudflare evaluation instance</strong><p>Ranch Hand will create a new dedicated D1 database, apply the verified migrations, upload the immutable Worker and web assets through Cloudflare's native API, configure the published schedules, and expose only Cloudflare-managed workers.dev HTTPS. Existing resources, custom domains, production secrets, and Cloudflare updates are not enabled in this adapter.</p><label>Fresh scoped Cloudflare API token<input type="password" required placeholder="Held in memory only and cleared after use" value={operationCloudflareToken} onChange={(event) => setOperationCloudflareToken(event.target.value)} /></label><label className="confirmation"><input type="checkbox" checked={installConfirmed} onChange={(event) => setInstallConfirmed(event.target.checked)} /> I understand this creates Cloudflare Worker and D1 resources in evaluation mode.</label><button type="button" disabled={!installConfirmed || !operationCloudflareToken || installing} onClick={installCloudflare}>{installing ? "Deploying and verifying Cloudflare…" : "Install Cloudflare evaluation"}</button></div>}
        {target === "remote-linux-compose" && targetReport?.ready && stagedBundle && !operationResult && <div className="inline-result install-panel"><strong>Install remote Linux evaluation instance</strong><p>Ranch Hand will transfer the verified Compose bundle through native SSH, add an ownership marker and Docker labels, bind RepoWrangler only to the remote host's loopback interface, and verify it through the pinned SSH connection. The dedicated directory and Compose project must be unused. This does not create a proxy or public ingress.</p><label>Fresh SSH private key file (optional with password)<input type="file" accept=".pem,.key" onChange={async (event) => { const file = event.target.files?.[0]; if (file && file.size > 1024 * 1024) { setPlanError("SSH private key file exceeds the 1 MiB safety limit"); return; } const contents = file ? await file.text() : ""; setOperationSSHCredentials((current) => ({ ...current, sshPrivateKey: contents })); }} /></label><label>Private-key passphrase (optional)<input type="password" placeholder="Held in memory only" value={operationSSHCredentials.sshPrivateKeyPassphrase || ""} onChange={(event) => setOperationSSHCredentials({ ...operationSSHCredentials, sshPrivateKeyPassphrase: event.target.value })} /></label><label>SSH password (optional with key)<input type="password" placeholder="Held in memory only" value={operationSSHCredentials.sshPassword || ""} onChange={(event) => setOperationSSHCredentials({ ...operationSSHCredentials, sshPassword: event.target.value })} /></label><label className="confirmation"><input type="checkbox" checked={installConfirmed} onChange={(event) => setInstallConfirmed(event.target.checked)} /> I understand this creates a loopback-only evaluation project on the selected Linux host.</label><button type="button" disabled={!installConfirmed || (!operationSSHCredentials.sshPrivateKey && !operationSSHCredentials.sshPassword) || installing} onClick={installRemote}>{installing ? "Deploying and verifying remote host…" : "Install remote Linux evaluation"}</button></div>}
        {operationResult && operationKind === "install" && <div className="inline-result success"><strong>Docker Desktop RepoWrangler installation committed</strong><p>The container passed its readiness check and the lifecycle journal is {operationResult.operation.journal.phase}. Open <a href={`http://${planResult?.configuration.listenAddress}`} target="_blank" rel="noreferrer">http://{planResult?.configuration.listenAddress}</a>.</p><button type="button" className="secondary" disabled={installing} onClick={backupLocal}>{installing ? "Creating consistent backup…" : "Back up local data"}</button></div>}
        {operationResult && operationKind === "wsl-install" && <div className="inline-result success"><strong>WSL Docker Compose installation committed</strong><p>Docker Compose started the verified RepoWrangler release inside {planResult?.configuration.distribution}. Open <a href="http://127.0.0.1:8080" target="_blank" rel="noreferrer">http://127.0.0.1:8080</a>.</p></div>}
        {operationResult && operationKind === "backup" && <div className="inline-result success"><strong>Consistent local backup committed</strong><p>Ranch Hand archived the managed container's persistent data while preserving its original running or stopped state. A running container was restarted and readiness-verified. The lifecycle journal is {operationResult.operation.journal.phase}.</p>{operationResult.operation.backup && <dl><div><dt>Archive</dt><dd>{operationResult.operation.backup.artifact.locator}</dd></div><div><dt>Size</dt><dd>{operationResult.operation.backup.artifact.size.toLocaleString()} bytes</dd></div><div><dt>SHA-256</dt><dd className="digest">{operationResult.operation.backup.artifact.sha256}</dd></div></dl>}</div>}
        {operationResult && operationKind === "update" && <div className="inline-result success"><strong>Backup-first local update committed</strong><p>The new immutable container passed readiness verification. The prior container and volume remain stopped in the rollback pool, and the lifecycle journal is {operationResult.operation.journal.phase}.</p>{operationResult.operation.backup && <dl><div><dt>Rollback archive</dt><dd>{operationResult.operation.backup.artifact.locator}</dd></div><div><dt>Size</dt><dd>{operationResult.operation.backup.artifact.size.toLocaleString()} bytes</dd></div><div><dt>SHA-256</dt><dd className="digest">{operationResult.operation.backup.artifact.sha256}</dd></div></dl>}<button type="button" className="secondary" disabled={installing} onClick={backupLocal}>{installing ? "Creating consistent backup…" : "Back up updated local data"}</button></div>}
        {operationResult && (operationKind === "restore" || operationKind === "rollback") && <div className="inline-result success"><strong>{operationKind === "restore" ? "Backup-first local restore committed" : "Backup-first local rollback committed"}</strong><p>The selected archive was verified and restored into a new owned volume, the exact target release passed readiness verification, and the replaced container remains preserved in the rollback pool. The lifecycle journal is {operationResult.operation.journal.phase}.</p>{operationResult.operation.backup && <dl><div><dt>Fresh safety archive</dt><dd>{operationResult.operation.backup.artifact.locator}</dd></div><div><dt>Size</dt><dd>{operationResult.operation.backup.artifact.size.toLocaleString()} bytes</dd></div><div><dt>SHA-256</dt><dd className="digest">{operationResult.operation.backup.artifact.sha256}</dd></div></dl>}</div>}
        {operationResult && operationKind === "repair" && <div className="inline-result success"><strong>Backup-first local repair committed</strong><p>Ranch Hand created a fresh consistent archive, reconstructed the same immutable release in a new owned volume, passed readiness and exact-version verification, and preserved the replaced container and untouched volume. The lifecycle journal is {operationResult.operation.journal.phase}.</p>{operationResult.operation.backup && <dl><div><dt>Fresh safety archive</dt><dd>{operationResult.operation.backup.artifact.locator}</dd></div><div><dt>Size</dt><dd>{operationResult.operation.backup.artifact.size.toLocaleString()} bytes</dd></div><div><dt>SHA-256</dt><dd className="digest">{operationResult.operation.backup.artifact.sha256}</dd></div></dl>}</div>}
        {operationResult && operationKind === "azure-install" && <div className="inline-result success"><strong>Azure evaluation installation committed</strong><p>The ARM deployment, digest-pinned image, Azure-managed HTTPS endpoint, readiness, and exact immutable release identity passed verification. The lifecycle journal is {operationResult.operation.journal.phase}.</p></div>}
        {operationResult && operationKind === "cloudflare-install" && <div className="inline-result success"><strong>Cloudflare evaluation installation committed</strong><p>The D1 ownership marker and migrations, Worker module and assets, schedules, Cloudflare-managed HTTPS endpoint, readiness, and exact immutable release identity passed verification. The lifecycle journal is {operationResult.operation.journal.phase}.</p></div>}
        {operationResult && operationKind === "remote-install" && <div className="inline-result success"><strong>Remote Linux evaluation installation committed</strong><p>The transferred files, target-side ownership marker, Docker labels, immutable image, loopback binding, SSH-forwarded readiness, and exact release identity passed verification. The lifecycle journal is {operationResult.operation.journal.phase}.</p></div>}
      </section>}
      <section className="grid" aria-label="Initial deployment targets">
        {[
          ["Local Docker Compose — WSL", "Docker Engine and Compose inside a local WSL2 distribution; no Docker Desktop or SSH."],
          ["Local Docker Desktop", "Docker Desktop's Windows-exposed Linux container engine."],
          ["Remote Linux Docker Compose", "A Linux VM or server managed securely over SSH."],
          ["Cloudflare", "The reference Worker, D1, and static web profile."],
          ["Azure Container Apps", "Azure-native HTTPS and managed runtime."],
        ].map(([name, detail]) => <article key={name}><h3>{name}</h3><p>{detail}</p><span>Evaluation install available</span></article>)}
      </section>
      <footer>Ranch Hand runs on loopback only. Deployment credentials are never stored in deployment plans.</footer>
    </main>
  );
}

createRoot(document.getElementById("root")!).render(<React.StrictMode><App /></React.StrictMode>);
