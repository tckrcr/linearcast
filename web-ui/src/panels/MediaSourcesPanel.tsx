import { FormEvent, ReactNode, useEffect, useRef, useState } from "react";
import {
  clearJellyfinConfig,
  clearPlexConfig,
  getJellyfinLibraries,
  getJellyfinStatus,
  getPlexLibraries,
  getPlexStatus,
  pollPlexPin,
  setJellyfinConfig,
  setPlexConfig,
  startJellyfinScan,
  startPlexPin,
  startPlexScan,
} from "../api";
import {
  cancelIngest,
  deleteLocalMediaSource,
  getLocalMediaSources,
  pollIngest,
  saveLocalMediaSource,
  startAllLocalMediaSourcesScan,
  startLocalMediaSourceScan,
  type IngestJob,
} from "../api/media";
import { Dialog } from "../Dialog";
import { formatMs } from "../format";
import { usePolling } from "../hooks/usePolling";
import type { JellyfinStatus, LocalMediaSource, MediaLibrary, PlexServerConnection, PlexStatus } from "../types";
import styles from "./MediaSourcesPanel.module.css";

type DialogKind = "none" | "picker" | "plex" | "jellyfin" | "local";
type ScanKind = "none" | "plex" | "jellyfin";
type PathReplacementPair = { id: string; source: string; local: string };
type PlexAuthLaunch = { id: number; window: Window | null };

let nextPathReplacementID = 1;
let nextPlexAuthLaunchID = 1;
const SCAN_STORAGE_PREFIX = "linearcast:scan:";
const PLEX_AUTH_WINDOW_FEATURES = "popup,width=520,height=720,menubar=no,toolbar=no,location=yes,status=no,scrollbars=yes,resizable=yes";

type PersistedScan = { jobId: string; startedAtMs: number };

function scanStorageKey(source: string) {
  return `${SCAN_STORAGE_PREFIX}${source}`;
}

function loadPersistedScan(key: string): PersistedScan | null {
  try {
    const raw = window.localStorage.getItem(key);
    if (!raw) return null;
    const parsed = JSON.parse(raw) as Partial<PersistedScan>;
    if (!parsed.jobId || !parsed.startedAtMs) return null;
    return { jobId: parsed.jobId, startedAtMs: parsed.startedAtMs };
  } catch {
    return null;
  }
}

function savePersistedScan(key: string, scan: PersistedScan) {
  try {
    window.localStorage.setItem(key, JSON.stringify(scan));
  } catch {
    // Scans still work without browser persistence.
  }
}

function clearPersistedScan(key: string) {
  try {
    window.localStorage.removeItem(key);
  } catch {
    // Nothing to clear if browser persistence is unavailable.
  }
}

function initialServerScan(): ScanKind {
  if (loadPersistedScan(scanStorageKey("plex"))) return "plex";
  if (loadPersistedScan(scanStorageKey("jellyfin"))) return "jellyfin";
  return "none";
}

const LOCAL_KIND_LABELS: Record<LocalMediaSource["mediaKind"], string> = {
  movies: "movies",
  shows: "tv",
  music: "music",
  filler: "filler",
};

function localKindLabel(kind: LocalMediaSource["mediaKind"]) {
  return LOCAL_KIND_LABELS[kind] ?? kind;
}

function newPathReplacementPair(source = "", local = ""): PathReplacementPair {
  const id = `path-replacement-${nextPathReplacementID++}`;
  return { id, source, local };
}

function parsePathReplacementPairs(spec: string): PathReplacementPair[] {
  const trimmed = spec.trim();
  if (!trimmed) return [newPathReplacementPair()];
  const pairs = trimmed
    .split(/[;,]/)
    .map((raw) => raw.trim())
    .filter(Boolean)
    .map((raw) => {
      const eq = raw.indexOf("=");
      if (eq === -1) return newPathReplacementPair(raw, "");
      return newPathReplacementPair(raw.slice(0, eq).trim(), raw.slice(eq + 1).trim());
    });
  return pairs.length ? pairs : [newPathReplacementPair()];
}

function serializePathReplacementPairs(pairs: PathReplacementPair[]) {
  return pairs
    .map((pair) => ({ source: pair.source.trim(), local: pair.local.trim() }))
    .filter((pair) => pair.source || pair.local)
    .map((pair) => `${pair.source}=${pair.local}`)
    .join(";");
}

function hasIncompletePathReplacementPair(pairs: PathReplacementPair[]) {
  return pairs.some((pair) => {
    const source = pair.source.trim();
    const local = pair.local.trim();
    return (source && !local) || (!source && local);
  });
}

function openPlexAuthLaunchWindow(): PlexAuthLaunch {
  const authWindow = window.open("about:blank", "linearcast-plex-auth", PLEX_AUTH_WINDOW_FEATURES);
  if (authWindow) {
    try {
      authWindow.document.title = "Plex sign-in";
      authWindow.document.body.innerHTML = '<p style="font-family: system-ui, sans-serif; padding: 16px;">Starting Plex sign-in...</p>';
    } catch {
      // The popup may already have navigated; the dialog still owns the handle.
    }
    authWindow.focus();
  }
  return { id: nextPlexAuthLaunchID++, window: authWindow };
}

export function MediaSourcesPanel() {
  const [plex, setPlex] = useState<PlexStatus>({ connected: false });
  const [jellyfin, setJellyfin] = useState<JellyfinStatus>({ connected: false, configured: false });
  const [localSources, setLocalSources] = useState<LocalMediaSource[]>([]);
  const [editingLocal, setEditingLocal] = useState<LocalMediaSource | null>(null);
  const [loaded, setLoaded] = useState(false);
  const [dialog, setDialog] = useState<DialogKind>("none");
  const [plexAuthLaunch, setPlexAuthLaunch] = useState<PlexAuthLaunch | null>(null);
  const [scan, setScan] = useState<ScanKind>(() => initialServerScan());
  const [error, setError] = useState("");
  const allScan = useScanJob(scanStorageKey("local:all"));
  const allScanRunning = allScan.job?.status === "running";

  async function scanAllLocalSources() {
    await allScan.start(() => startAllLocalMediaSourcesScan());
  }

  useEffect(() => {
    let cancelled = false;
    Promise.all([getPlexStatus(), getJellyfinStatus(), getLocalMediaSources()])
      .then(([p, j, local]) => {
        if (cancelled) return;
        setPlex(p);
        setJellyfin(j);
        setLocalSources(local);
      })
      .catch((err) => {
        if (!cancelled) setError(err instanceof Error ? err.message : String(err));
      })
      .finally(() => {
        if (!cancelled) setLoaded(true);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  async function signOutPlex() {
    setError("");
    try {
      const next = await clearPlexConfig();
      setPlex(next);
      if (scan === "plex") setScan("none");
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }

  async function signOutJellyfin() {
    setError("");
    try {
      const next = await clearJellyfinConfig();
      setJellyfin(next);
      if (scan === "jellyfin") setScan("none");
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }

  async function removeLocalSource(source: LocalMediaSource) {
    if (!window.confirm(`Delete local source "${source.name}"?`)) return;
    setError("");
    try {
      await deleteLocalMediaSource(source.id);
      setLocalSources((prev) => prev.filter((item) => item.id !== source.id));
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }

  function closeDialog() {
    setDialog("none");
    setEditingLocal(null);
  }

  function beginPlexConnection() {
    setPlexAuthLaunch(openPlexAuthLaunchWindow());
    setDialog("plex");
  }

  return (
    <section className="admin-panel-section" aria-label="Media sources">
      <div className="section-headline">
        <div className="section-headline-main">
          <h2>Media sources</h2>
          <p className="section-purpose">
            Where Linearcast scans for media. Add local folders or connect Plex/Jellyfin, then scan
            to populate the inventory.
          </p>
        </div>
        <div className="section-headline-actions">
          {localSources.length > 0 &&
            (allScanRunning ? (
              <button type="button" onClick={() => void allScan.cancel()}>
                Cancel scan
              </button>
            ) : (
              <button type="button" disabled={!loaded} onClick={() => void scanAllLocalSources()}>
                Scan all sources
              </button>
            ))}
          <button type="button" disabled={!loaded} onClick={() => setDialog("picker")}>
            + Add media source
          </button>
        </div>
      </div>
      {(allScan.job || allScan.startError) && (
        <ScanResult job={allScan.job} error={allScan.startError} startedAtMs={allScan.startedAtMs} />
      )}

      <div className={styles["source-list"]}>
        {plex.connected && (
          <PlexCard
            status={plex}
            onReplace={beginPlexConnection}
            onSignOut={() => void signOutPlex()}
            scanOpen={scan === "plex"}
            onToggleScan={() => setScan((s) => (s === "plex" ? "none" : "plex"))}
          />
        )}
        {jellyfin.connected && (
          <JellyfinCard
            status={jellyfin}
            onReplace={() => setDialog("jellyfin")}
            onSignOut={() => void signOutJellyfin()}
            scanOpen={scan === "jellyfin"}
            onToggleScan={() => setScan((s) => (s === "jellyfin" ? "none" : "jellyfin"))}
          />
        )}
        {localSources.map((source) => (
          <LocalCard
            key={source.id}
            source={source}
            onReplace={() => {
              setEditingLocal(source);
              setDialog("local");
            }}
            onDelete={() => void removeLocalSource(source)}
          />
        ))}
      </div>
      {loaded && !plex.connected && !jellyfin.connected && localSources.length === 0 && (
        <p className="muted">No media sources configured. Click “+ Add media source” to connect Plex, Jellyfin, or Local.</p>
      )}
      {error && <div className="plex-token-error">{error}</div>}

      <Dialog open={dialog === "picker"} onClose={closeDialog} title="Add media source">
        <div className={styles["kind-picker"]}>
          <button
            type="button"
            className={styles["kind-option"]}
            disabled={plex.connected}
            onClick={beginPlexConnection}
            title={plex.connected ? "Plex already connected" : undefined}
          >
            <span className={styles["plex-logo"]} aria-hidden="true">P</span>
            <span>Plex{plex.connected ? " (connected)" : ""}</span>
          </button>
          <button
            type="button"
            className={styles["kind-option"]}
            disabled={jellyfin.connected}
            onClick={() => setDialog("jellyfin")}
            title={jellyfin.connected ? "Jellyfin already connected" : undefined}
          >
            <span className={`${styles["plex-logo"]} ${styles["jellyfin-logo"]}`} aria-hidden="true">J</span>
            <span>Jellyfin{jellyfin.connected ? " (connected)" : ""}</span>
          </button>
          <button
            type="button"
            className={styles["kind-option"]}
            onClick={() => {
              setEditingLocal(null);
              setDialog("local");
            }}
          >
            <span className={styles["plex-logo"]} aria-hidden="true">L</span>
            <span>Local</span>
          </button>
        </div>
      </Dialog>

      <PlexFormDialog
        open={dialog === "plex"}
        initial={plex}
        authLaunch={plexAuthLaunch}
        onClose={closeDialog}
        onBack={plex.connected ? undefined : () => setDialog("picker")}
        onSaved={(next) => {
          setPlex(next);
          setDialog("none");
          setScan("plex");
        }}
      />
      <JellyfinFormDialog
        open={dialog === "jellyfin"}
        initial={jellyfin}
        onClose={closeDialog}
        onBack={jellyfin.connected ? undefined : () => setDialog("picker")}
        onSaved={(next) => {
          setJellyfin(next);
          setDialog("none");
        }}
      />
      <LocalFormDialog
        open={dialog === "local"}
        initial={editingLocal}
        onClose={closeDialog}
        onBack={editingLocal ? undefined : () => setDialog("picker")}
        onSaved={(next) => {
          setLocalSources((prev) => {
            const idx = prev.findIndex((source) => source.id === next.id);
            if (idx === -1) return [...prev, next].sort((a, b) => a.name.localeCompare(b.name));
            const updated = [...prev];
            updated[idx] = next;
            return updated.sort((a, b) => a.name.localeCompare(b.name));
          });
          closeDialog();
        }}
      />
    </section>
  );
}

type PlexCardProps = {
  status: PlexStatus;
  onReplace: () => void;
  onSignOut: () => void;
  scanOpen: boolean;
  onToggleScan: () => void;
};

function PlexCard({ status, onReplace, onSignOut, scanOpen, onToggleScan }: PlexCardProps) {
  const account = [status.username, status.serverName].filter(Boolean).join(" / ");
  return (
    <div className={styles["media-server-section"]}>
      <div className={styles["media-server-connected-row"]}>
        <span className={styles["plex-logo"]} aria-hidden="true">P</span>
        <strong>Plex</strong>
        {account && <span className="muted plex-account">{account}</span>}
        <div className={styles["plex-token-actions"]}>
          <button type="button" className="link-button" onClick={onToggleScan}>
            Scan media
          </button>
          <button type="button" className="link-button" onClick={onReplace}>
            Replace
          </button>
          <button type="button" className="link-button" onClick={onSignOut}>
            Sign out
          </button>
        </div>
      </div>
      {status.url && <div className="muted media-server-detail">{status.url}</div>}
      {status.pathMap && <div className="muted media-server-detail">path replacement: {status.pathMap}</div>}
      {scanOpen && (
        <ScanPanel
          source="plex"
          onFetchLibraries={getPlexLibraries}
          onStartScan={(libId, maxRes) => startPlexScan(libId, maxRes)}
          onClose={onToggleScan}
        />
      )}
    </div>
  );
}

type JellyfinCardProps = {
  status: JellyfinStatus;
  onReplace: () => void;
  onSignOut: () => void;
  scanOpen: boolean;
  onToggleScan: () => void;
};

function JellyfinCard({ status, onReplace, onSignOut, scanOpen, onToggleScan }: JellyfinCardProps) {
  const server = [status.serverName, status.version].filter(Boolean).join(" / ");
  return (
    <div className={styles["media-server-section"]}>
      <div className={styles["media-server-connected-row"]}>
        <span className={`${styles["plex-logo"]} ${styles["jellyfin-logo"]}`} aria-hidden="true">J</span>
        <strong>Jellyfin</strong>
        {server && <span className="muted plex-account">{server}</span>}
        <div className={styles["plex-token-actions"]}>
          <button type="button" className="link-button" onClick={onToggleScan}>
            Scan media
          </button>
          <button type="button" className="link-button" onClick={onReplace}>
            Replace
          </button>
          <button type="button" className="link-button" onClick={onSignOut}>
            Sign out
          </button>
        </div>
      </div>
      {status.url && <div className="muted media-server-detail">{status.url}</div>}
      {status.pathMap && <div className="muted media-server-detail">path replacement: {status.pathMap}</div>}
      {scanOpen && (
        <ScanPanel
          source="jellyfin"
          onFetchLibraries={getJellyfinLibraries}
          onStartScan={(libId, maxRes) => startJellyfinScan(libId, maxRes)}
          onClose={onToggleScan}
        />
      )}
    </div>
  );
}

type LocalCardProps = {
  source: LocalMediaSource;
  onReplace: () => void;
  onDelete: () => void;
};

function FolderIcon() {
  return (
    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round">
      <path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z" />
    </svg>
  );
}

function LocalCard({ source, onReplace, onDelete }: LocalCardProps) {
  const scan = useScanJob(scanStorageKey(`local:${source.id}`));

  async function startScan() {
    await scan.start(() => startLocalMediaSourceScan(source.id));
  }

  const [primaryPath, ...extraPaths] = source.paths;
  const running = scan.job?.status === "running";

  return (
    <div className={styles["media-server-section"]}>
      <div className={styles["media-server-connected-row"]}>
        <span className={`${styles["plex-logo"]} ${styles["local-logo"]}`} aria-hidden="true">
          <FolderIcon />
        </span>
        <strong>{source.name}</strong>
        <span className={styles["local-kind-pill"]}>{localKindLabel(source.mediaKind)}</span>
        {primaryPath && <span className="muted plex-account">{primaryPath}</span>}
        <div className={styles["plex-token-actions"]}>
          {running ? (
            <>
              <button type="button" className="link-button" onClick={() => void scan.cancel()}>
                Cancel
              </button>
            </>
          ) : (
            <button type="button" className="link-button" onClick={() => void startScan()}>
              Scan media
            </button>
          )}
          <button type="button" className="link-button" disabled={running} onClick={onReplace}>
            Replace
          </button>
          <button type="button" className="link-button" disabled={running} onClick={onDelete}>
            Delete
          </button>
        </div>
      </div>
      {extraPaths.map((path) => (
        <div key={path} className="muted media-server-detail">{path}</div>
      ))}
      <ScanResult job={scan.job} error={scan.startError} startedAtMs={scan.startedAtMs} />
    </div>
  );
}

// useScanJob owns the poll/cancel lifecycle for one source's ingest job. It
// persists the active job ID so running scan progress survives panel navigation
// and refreshes while the backend job is still alive.
function useScanJob(storageKey: string) {
  const [jobId, setJobId] = useState<string | null>(null);
  const [job, setJob] = useState<IngestJob | null>(null);
  const [startedAtMs, setStartedAtMs] = useState<number | null>(null);
  const [startError, setStartError] = useState("");

  useEffect(() => {
    const persisted = loadPersistedScan(storageKey);
    if (!persisted) return;
    setJobId(persisted.jobId);
    setStartedAtMs(persisted.startedAtMs);
    setJob({ status: "running" });
  }, [storageKey]);

  usePolling({
    enabled: !!jobId && job?.status === "running",
    intervalMs: 1000,
    maxIntervalMs: 15_000,
    immediate: false,
    resetKey: jobId,
    task: async (signal) => {
      if (!jobId) return "stop";
      try {
        const next = await pollIngest(jobId, signal);
        setJob(next);
        if (next.status !== "running") {
          clearPersistedScan(storageKey);
          return "stop";
        }
      } catch (err) {
        if (signal.aborted) return "stop";
        setStartError(err instanceof Error ? err.message : String(err));
        clearPersistedScan(storageKey);
        setJobId(null);
        setJob(null);
        setStartedAtMs(null);
        return "stop";
      }
    },
  });

  async function start(launch: () => Promise<{ jobId: string }>) {
    const started = Date.now();
    setStartError("");
    setStartedAtMs(started);
    setJob({ status: "running" });
    try {
      const { jobId: id } = await launch();
      savePersistedScan(storageKey, { jobId: id, startedAtMs: started });
      setJobId(id);
    } catch (err) {
      clearPersistedScan(storageKey);
      setJob(null);
      setJobId(null);
      setStartedAtMs(null);
      setStartError(err instanceof Error ? err.message : String(err));
    }
  }

  async function cancel() {
    if (!jobId) return;
    try {
      await cancelIngest(jobId);
    } catch (err) {
      setStartError(err instanceof Error ? err.message : String(err));
    }
  }

  return { job, startedAtMs, startError, start, cancel };
}

function ScanResult({ job, error, startedAtMs }: { job: IngestJob | null; error: string; startedAtMs: number | null }) {
  if (error) {
    return <div className={styles["ingest-log"]}><div className={styles["ingest-log-summary"]}><span className="danger">{error}</span></div></div>;
  }
  if (!job) return null;
  if (job.status === "running") {
    if (job.total == null || job.processed == null) return null;
    const pct = job.total > 0 ? Math.floor((job.processed / job.total) * 100) : 0;
    const etaMs = estimateScanEtaMs(job, startedAtMs);
    return (
      <div className={styles["ingest-log"]}>
        <div className={styles["ingest-log-summary"]}>
          <span className="muted">
            <span className={styles["scan-spinner"]} aria-hidden="true" /> {job.processed} / {job.total} ({pct}%)
            {etaMs != null ? ` · ETA ${formatMs(etaMs)}` : ""}
          </span>
          <progress className={styles["scan-progress"]} value={job.processed} max={job.total} />
        </div>
      </div>
    );
  }
  const summary = job.summary;
  const reasons = summary?.failuresByReason ?? {};
  const reasonEntries = Object.entries(reasons).sort((a, b) => b[1] - a[1]);
  const statusLabel = job.status === "cancelled" ? "Scan cancelled" : "Scan complete";
  return (
    <div className={styles["ingest-log"]}>
      <div className={styles["ingest-log-summary"]}>
        {summary ? (
          <span className="muted">
            {statusLabel}: {summary.passed}/{summary.total} passed
            {summary.failed > 0 ? `, ${summary.failed} failed` : ""}
          </span>
        ) : (
          <span className="muted">{statusLabel}</span>
        )}
        {!summary && job.error && <span className="danger">{job.error}</span>}
      </div>
      {reasonEntries.length > 0 && (
        <ul className={styles["ingest-failure-reasons"]}>
          {reasonEntries.map(([reason, count]) => (
            <li key={reason}><span className="muted">{count} × </span>{reason}</li>
          ))}
        </ul>
      )}
      {job.logPath && <div className="muted media-server-detail">Full log: {job.logPath}</div>}
    </div>
  );
}


function estimateScanEtaMs(job: IngestJob, startedAtMs: number | null) {
  if (!startedAtMs || job.processed == null || job.total == null || job.processed <= 0) return null;
  const remaining = job.total - job.processed;
  if (remaining <= 0) return 0;
  const elapsedMs = Date.now() - startedAtMs;
  if (elapsedMs <= 0) return null;
  const msPerItem = elapsedMs / job.processed;
  return remaining * msPerItem;
}

type PlexFormDialogProps = {
  open: boolean;
  initial: PlexStatus;
  authLaunch: PlexAuthLaunch | null;
  onClose: () => void;
  onSaved: (next: PlexStatus) => void;
  onBack?: () => void;
};

function plexServerOptionLabel(server: PlexServerConnection) {
  let endpoint = server.url;
  try {
    const parsed = new URL(server.url);
    let host = parsed.hostname;
    const firstHostPart = host.split(".")[0] || host;
    if (/^\d+-\d+-\d+-\d+$/.test(firstHostPart) && host.endsWith(".plex.direct")) {
      host = firstHostPart.replaceAll("-", ".");
    }
    endpoint = parsed.port ? `${host}:${parsed.port}` : host;
  } catch {
    // Keep the original URL if it is not parseable; connect validation will
    // still reject unusable values before storing them.
  }
  return `${server.name || "Plex"} ${server.local ? "local" : "remote"} ${endpoint}`;
}

function PlexFormDialog({ open, initial, authLaunch, onClose, onSaved, onBack }: PlexFormDialogProps) {
  const [pin, setPin] = useState<{ id: number; code: string; authUrl: string } | null>(null);
  const [servers, setServers] = useState<PlexServerConnection[]>([]);
  const [selectedURL, setSelectedURL] = useState("");
  const [pathPairs, setPathPairs] = useState(() => parsePathReplacementPairs(initial.connected ? initial.pathMap || "" : ""));
  const [showAdvanced, setShowAdvanced] = useState(false);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");
  const [authWindowOpen, setAuthWindowOpen] = useState(false);
  const authWindowRef = useRef<Window | null>(null);

  function authWindowClosed(authWindow: Window) {
    try {
      return authWindow.closed;
    } catch {
      return true;
    }
  }

  function closeAuthWindow() {
    const authWindow = authWindowRef.current;
    authWindowRef.current = null;
    setAuthWindowOpen(false);
    if (!authWindow || authWindowClosed(authWindow)) return;
    try {
      authWindow.close();
    } catch {
      // Keep going; if the browser kept the cross-origin page open, replace it
      // with a blank page and try one more close after navigation.
    }
    window.setTimeout(() => {
      if (authWindowClosed(authWindow)) return;
      try {
        authWindow.location.replace("about:blank");
      } catch {
        return;
      }
      window.setTimeout(() => {
        if (!authWindowClosed(authWindow)) authWindow.close();
      }, 50);
    }, 50);
  }

  function navigateAuthWindow(authUrl: string) {
    const authWindow = authWindowRef.current;
    if (!authWindow || authWindowClosed(authWindow)) {
      setAuthWindowOpen(false);
      setMessage("Plex sign-in window is not open. Close this dialog and click Plex again.");
      return;
    }
    try {
      authWindow.location.replace(authUrl);
    } catch {
      try {
        authWindow.location.href = authUrl;
      } catch {
        setAuthWindowOpen(false);
        setMessage("Plex sign-in window could not be opened. Close this dialog and click Plex again.");
        return;
      }
    }
    authWindow.focus();
    setAuthWindowOpen(true);
  }

  useEffect(() => {
    if (!open) return;
    closeAuthWindow();
    authWindowRef.current = authLaunch?.window ?? null;
    setPin(null);
    setServers([]);
    setSelectedURL("");
    setPathPairs(parsePathReplacementPairs(initial.connected ? initial.pathMap || "" : ""));
    setShowAdvanced(Boolean(initial.connected && initial.pathMap));
    setAuthWindowOpen(Boolean(authLaunch?.window && !authWindowClosed(authLaunch.window)));
    setMessage(authLaunch && !authLaunch.window ? "Plex sign-in was blocked. Allow popups for this site, then close this dialog and click Plex again." : "");
    if (!authLaunch?.window) {
      setBusy(false);
      return;
    }
    setBusy(true);
    startPlexPin()
      .then((next) => {
        setPin(next);
        navigateAuthWindow(next.authUrl);
      })
      .catch((err) => setMessage(err instanceof Error ? err.message : String(err)))
      .finally(() => setBusy(false));
  }, [open, initial, authLaunch]);

  useEffect(() => {
    if (open) return;
    closeAuthWindow();
  }, [open]);

  usePolling({
    enabled: open && !!pin && servers.length === 0,
    intervalMs: 1000,
    maxIntervalMs: 3000,
    immediate: true,
    visibleOnly: false,
    resetKey: pin?.id ?? "none",
    task: async (signal) => {
      if (!pin) return "stop";
      try {
        const next = await pollPlexPin(pin.id, pin.code, signal);
        if (!next.authorized) return;
        const discovered = next.servers ?? [];
        closeAuthWindow();
        window.focus();
        setServers(discovered);
        setSelectedURL((current) => current || discovered[0]?.url || "");
        if (discovered.length === 0) setMessage("Plex sign-in succeeded, but no Plex server connections were found.");
        return "stop";
      } catch (err) {
        if (signal.aborted) return "stop";
        setMessage(err instanceof Error ? err.message : String(err));
        return "stop";
      }
    },
  });

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (hasIncompletePathReplacementPair(pathPairs)) {
      setMessage("Each path replacement row needs both a server path and a local path.");
      return;
    }
    const selected = servers.find((server) => server.url === selectedURL);
    if (!selected) {
      setMessage("Choose a Plex server.");
      return;
    }
    setBusy(true);
    setMessage("");
    try {
      const next = await setPlexConfig(selected.url, selected.token, serializePathReplacementPairs(pathPairs));
      onSaved(next);
    } catch (err) {
      setMessage(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }

  const selectedServer = servers.find((server) => server.url === selectedURL);

  return (
    <Dialog open={open} onClose={onClose} title={initial.connected ? "Replace Plex connection" : "Add Plex"}>
      <form className={styles["dialog-form"]} onSubmit={(event) => void submit(event)}>
        {!pin && !message && <span className="muted">Starting Plex sign-in…</span>}
        {pin && servers.length === 0 && (
          <div className={styles["dialog-field"]}>
            <span>Sign in to Plex</span>
            <p className="muted">
              {authWindowOpen
                ? "Plex sign-in is open in a separate window. Authorize linearcast there; this dialog will continue automatically."
                : "Waiting for the Plex sign-in window."}
            </p>
            <small className="muted">Code: {pin.code}</small>
          </div>
        )}
        {servers.length > 0 && (
          <>
            <label>
              <span>Plex server</span>
              <select value={selectedURL} disabled={busy} onChange={(e) => setSelectedURL(e.target.value)} required>
                {servers.map((server) => (
                  <option key={server.url} value={server.url}>
                    {plexServerOptionLabel(server)}
                  </option>
                ))}
              </select>
              {selectedServer && <small className="muted">{selectedServer.url}</small>}
            </label>
            <AdvancedDisclosure open={showAdvanced} onOpenChange={setShowAdvanced}>
              <div className={styles["dialog-field"]}>
                <span>Path replacement</span>
                <PathReplacementEditor
                  pairs={pathPairs}
                  disabled={busy}
                  sourceName="Plex"
                  sourcePlaceholder="/path/on/plex"
                  localPlaceholder="/path/on/local"
                  onChange={setPathPairs}
                />
                <small className="muted">
                  Maps a path on the Plex server to a path this server can read on disk. Leave blank if they're identical.
                </small>
              </div>
            </AdvancedDisclosure>
          </>
        )}
        {message && <div className="plex-token-error">{message}</div>}
        <div className={styles["dialog-actions"]}>
          {onBack && (
            <button type="button" className={`link-button ${styles["dialog-back"]}`} disabled={busy} onClick={onBack}>
              ‹ Back
            </button>
          )}
          <button type="button" className="link-button" disabled={busy} onClick={onClose}>
            Cancel
          </button>
          <button type="submit" disabled={busy || servers.length === 0}>
            {busy ? "Saving…" : "Connect"}
          </button>
        </div>
      </form>
    </Dialog>
  );
}

function AdvancedDisclosure({
  open,
  onOpenChange,
  children,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  children: ReactNode;
}) {
  return (
    <details
      className={styles["advanced-disclosure"]}
      open={open}
      onToggle={(e) => onOpenChange(e.currentTarget.open)}
    >
      <summary className={styles["advanced-summary"]}>Advanced</summary>
      <div className={styles["advanced-body"]}>{children}</div>
    </details>
  );
}

type PathReplacementEditorProps = {
  pairs: PathReplacementPair[];
  disabled: boolean;
  sourceName: string;
  sourcePlaceholder: string;
  localPlaceholder: string;
  onChange: (pairs: PathReplacementPair[]) => void;
};

function PathReplacementEditor({
  pairs,
  disabled,
  sourceName,
  sourcePlaceholder,
  localPlaceholder,
  onChange,
}: PathReplacementEditorProps) {
  function updatePair(id: string, key: "source" | "local", value: string) {
    onChange(pairs.map((pair) => (pair.id === id ? { ...pair, [key]: value } : pair)));
  }

  function addPair() {
    onChange([...pairs, newPathReplacementPair()]);
  }

  function removePair(id: string) {
    const next = pairs.filter((pair) => pair.id !== id);
    onChange(next.length ? next : [newPathReplacementPair()]);
  }

  return (
    <div className={styles["path-replacement-editor"]}>
      <div className={styles["path-replacement-header"]} aria-hidden="true">
        <span>{sourceName} path</span>
        <span>Local path</span>
        <span />
      </div>
      {pairs.map((pair) => (
        <div key={pair.id} className={styles["path-replacement-row"]}>
          <input
            value={pair.source}
            disabled={disabled}
            placeholder={sourcePlaceholder}
            aria-label={`${sourceName} path`}
            onChange={(e) => updatePair(pair.id, "source", e.target.value)}
          />
          <input
            value={pair.local}
            disabled={disabled}
            placeholder={localPlaceholder}
            aria-label="Local path"
            onChange={(e) => updatePair(pair.id, "local", e.target.value)}
          />
          <button type="button" className="link-button" disabled={disabled} onClick={() => removePair(pair.id)}>
            Remove
          </button>
        </div>
      ))}
      <button type="button" className="link-button path-replacement-add" disabled={disabled} onClick={addPair}>
        + Add replacement
      </button>
    </div>
  );
}

type LocalFormDialogProps = {
  open: boolean;
  initial: LocalMediaSource | null;
  onClose: () => void;
  onSaved: (next: LocalMediaSource) => void;
  onBack?: () => void;
};

function LocalFormDialog({ open, initial, onClose, onSaved, onBack }: LocalFormDialogProps) {
  const [name, setName] = useState("");
  const [mediaKind, setMediaKind] = useState<LocalMediaSource["mediaKind"]>("movies");
  const [paths, setPaths] = useState<string[]>([""]);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");

  useEffect(() => {
    if (!open) return;
    setName(initial?.name ?? "");
    setMediaKind(initial?.mediaKind ?? "movies");
    setPaths(initial?.paths.length ? initial.paths : [""]);
    setMessage("");
    setBusy(false);
  }, [open, initial]);

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setBusy(true);
    setMessage("");
    try {
      const next = await saveLocalMediaSource({
        id: initial?.id,
        name,
        mediaKind,
        paths: paths.map((path) => path.trim()).filter(Boolean),
      });
      onSaved(next);
    } catch (err) {
      setMessage(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }

  function updatePath(index: number, value: string) {
    setPaths((prev) => prev.map((path, i) => (i === index ? value : path)));
  }

  function addPath(path = "") {
    setPaths((prev) => [...prev, path]);
  }

  function removePath(index: number) {
    setPaths((prev) => (prev.length <= 1 ? [""] : prev.filter((_, i) => i !== index)));
  }

  return (
    <Dialog open={open} onClose={onClose} title={initial ? "Replace Local source" : "Add Local source"}>
      <form className={styles["dialog-form"]} onSubmit={(event) => void submit(event)}>
        <label>
          <span>Name</span>
          <input
            value={name}
            disabled={busy}
            placeholder="Local"
            onChange={(e) => setName(e.target.value)}
            required
          />
        </label>
        <label>
          <span>Media kind</span>
          <select value={mediaKind} disabled={busy} onChange={(e) => setMediaKind(e.target.value as LocalMediaSource["mediaKind"])}>
            <option value="movies">movies</option>
            <option value="shows">tv</option>
            <option value="music">music</option>
            <option value="filler">filler</option>
          </select>
          <small className="muted">
            Selects how files are read: video (movies / tv), music, or filler. Movies vs TV episodes are detected
            automatically from filenames, so either video option scans the same way.
          </small>
        </label>
        <div className={styles["dialog-field"]}>
          <span>Paths</span>
          <small className="muted">Folders are scanned recursively — point at a top-level root to pick up everything beneath it.</small>
          <div className={styles["local-source-path-list"]}>
            {paths.map((path, index) => (
              <div key={index} className={styles["local-source-path-row"]}>
                <input
                  value={path}
                  disabled={busy}
                  placeholder="/path/to/media"
                  onChange={(e) => updatePath(index, e.target.value)}
                  required={index === 0}
                />
                <button
                  type="button"
                  className="link-button"
                  disabled={busy || paths.length <= 1}
                  onClick={() => removePath(index)}
                >
                  Remove
                </button>
              </div>
            ))}
            <button type="button" className="link-button path-replacement-add" disabled={busy} onClick={() => addPath()}>
              + Add path
            </button>
          </div>
        </div>
        {message && <div className="plex-token-error">{message}</div>}
        <div className={styles["dialog-actions"]}>
          {onBack && (
            <button type="button" className={`link-button ${styles["dialog-back"]}`} disabled={busy} onClick={onBack}>
              ‹ Back
            </button>
          )}
          <button type="button" className="link-button" disabled={busy} onClick={onClose}>
            Cancel
          </button>
          <button type="submit" disabled={busy}>
            {busy ? "Saving…" : "Save"}
          </button>
        </div>
      </form>
    </Dialog>
  );
}

type JellyfinFormDialogProps = {
  open: boolean;
  initial: JellyfinStatus;
  onClose: () => void;
  onSaved: (next: JellyfinStatus) => void;
  onBack?: () => void;
};

function JellyfinFormDialog({ open, initial, onClose, onSaved, onBack }: JellyfinFormDialogProps) {
  const [url, setURL] = useState(initial.url || "");
  const [apiKey, setAPIKey] = useState("");
  const [pathPairs, setPathPairs] = useState(() => parsePathReplacementPairs(initial.connected ? initial.pathMap || "" : ""));
  const [showAdvanced, setShowAdvanced] = useState(false);
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");

  useEffect(() => {
    if (!open) return;
    setURL(initial.url || "");
    setAPIKey("");
    setPathPairs(parsePathReplacementPairs(initial.connected ? initial.pathMap || "" : ""));
    setShowAdvanced(Boolean(initial.connected && initial.pathMap));
    setMessage("");
    setBusy(false);
  }, [open, initial]);

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (hasIncompletePathReplacementPair(pathPairs)) {
      setMessage("Each path replacement row needs both a server path and a local path.");
      return;
    }
    setBusy(true);
    setMessage("");
    try {
      const next = await setJellyfinConfig(url, apiKey, serializePathReplacementPairs(pathPairs));
      onSaved(next);
    } catch (err) {
      setMessage(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open={open} onClose={onClose} title={initial.connected ? "Replace Jellyfin connection" : "Add Jellyfin"}>
      <form className={styles["dialog-form"]} onSubmit={(event) => void submit(event)}>
        <label>
          <span>URL</span>
          <input
            type="url"
            value={url}
            disabled={busy}
            placeholder="http://jellyfin:8096"
            onChange={(e) => setURL(e.target.value)}
            required
          />
        </label>
        <label>
          <span>API key</span>
          <input
            type="password"
            value={apiKey}
            disabled={busy}
            onChange={(e) => setAPIKey(e.target.value)}
            required
          />
        </label>
        <AdvancedDisclosure open={showAdvanced} onOpenChange={setShowAdvanced}>
          <div className={styles["dialog-field"]}>
            <span>Path replacement</span>
            <PathReplacementEditor
              pairs={pathPairs}
              disabled={busy}
              sourceName="Jellyfin"
              sourcePlaceholder="/path/on/jellyfin"
              localPlaceholder="/path/on/local"
              onChange={setPathPairs}
            />
            <small className="muted">
              Maps a path on the Jellyfin server to a path this server can read on disk. Leave blank if they're identical.
            </small>
          </div>
        </AdvancedDisclosure>
        {message && <div className="plex-token-error">{message}</div>}
        <div className={styles["dialog-actions"]}>
          {onBack && (
            <button type="button" className={`link-button ${styles["dialog-back"]}`} disabled={busy} onClick={onBack}>
              ‹ Back
            </button>
          )}
          <button type="button" className="link-button" disabled={busy} onClick={onClose}>
            Cancel
          </button>
          <button type="submit" disabled={busy}>
            {busy ? "Saving…" : "Save"}
          </button>
        </div>
      </form>
    </Dialog>
  );
}

type ScanPanelProps = {
  source: "plex" | "jellyfin";
  onFetchLibraries: () => Promise<MediaLibrary[]>;
  onStartScan: (libraryId: string, maxResolution: string) => Promise<{ jobId: string }>;
  onClose: () => void;
};

function ScanPanel({ source, onFetchLibraries, onStartScan, onClose }: ScanPanelProps) {
  const [libraries, setLibraries] = useState<MediaLibrary[]>([]);
  const [loadingLibs, setLoadingLibs] = useState(true);
  const [libError, setLibError] = useState("");
  const [selectedLib, setSelectedLib] = useState("");
  const [maxResolution, setMaxResolution] = useState("1080");

  const scan = useScanJob(scanStorageKey(source));
  const running = scan.job?.status === "running";

  useEffect(() => {
    let cancelled = false;
    onFetchLibraries()
      .then((libs) => {
        if (cancelled) return;
        setLibraries(libs);
        if (libs.length > 0) {
          setSelectedLib(libs[0].key ?? libs[0].id ?? "");
        }
      })
      .catch((err) => {
        if (!cancelled) setLibError(err instanceof Error ? err.message : String(err));
      })
      .finally(() => {
        if (!cancelled) setLoadingLibs(false);
      });
    return () => {
      cancelled = true;
    };
  }, [onFetchLibraries]);

  async function startScan(event: FormEvent) {
    event.preventDefault();
    if (!selectedLib) return;
    await scan.start(() => onStartScan(selectedLib, maxResolution));
  }

  const libLabel = (lib: MediaLibrary) => lib.title ?? lib.name ?? lib.id ?? lib.key ?? "?";
  const libValue = (lib: MediaLibrary) => lib.key ?? lib.id ?? "";
  const placeholder = source === "plex" ? "Plex" : "Jellyfin";

  return (
    <div className={styles["scan-panel"]}>
      {loadingLibs && <span className="muted">loading {placeholder} libraries…</span>}
      {libError && <span className="plex-token-error">{libError}</span>}
      {!loadingLibs && !libError && (
        <form className={styles["scan-panel-form"]} onSubmit={(e) => void startScan(e)}>
          <select
            value={selectedLib}
            disabled={running}
            onChange={(e) => setSelectedLib(e.target.value)}
          >
            {libraries.map((lib) => (
              <option key={libValue(lib)} value={libValue(lib)}>
                {libLabel(lib)} ({lib.type})
              </option>
            ))}
          </select>
          <select
            value={maxResolution}
            disabled={running}
            onChange={(e) => setMaxResolution(e.target.value)}
          >
            <option value="">all resolutions</option>
            <option value="1080">1080p max (skip 4K)</option>
            <option value="720">720p max</option>
          </select>
          {running ? (
            <>
              <button type="button" className="link-button" onClick={() => void scan.cancel()}>
                Cancel
              </button>
            </>
          ) : (
            <button type="submit" disabled={!selectedLib}>
              Scan
            </button>
          )}
          <button type="button" className="link-button" disabled={running} onClick={onClose}>
            Close
          </button>
        </form>
      )}
      <ScanResult job={scan.job} error={scan.startError} startedAtMs={scan.startedAtMs} />
    </div>
  );
}
