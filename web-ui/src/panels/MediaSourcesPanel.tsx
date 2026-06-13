import { FormEvent, useEffect, useState } from "react";
import {
  clearJellyfinConfig,
  clearPlexConfig,
  getJellyfinLibraries,
  getJellyfinStatus,
  getPlexLibraries,
  getPlexStatus,
  setJellyfinConfig,
  setPlexConfig,
  startJellyfinScan,
  startPlexScan,
} from "../api";
import {
  browseFs,
  cancelIngest,
  deleteLocalMediaSource,
  getLocalMediaSources,
  pollIngest,
  saveLocalMediaSource,
  startLocalMediaSourceScan,
  type IngestJob,
} from "../api/media";
import { Dialog } from "../Dialog";
import { usePolling } from "../hooks/usePolling";
import type { JellyfinStatus, LocalMediaSource, MediaLibrary, PlexStatus } from "../types";
import styles from "./MediaSourcesPanel.module.css";

type DialogKind = "none" | "picker" | "plex" | "jellyfin" | "local";
type ScanKind = "none" | "plex" | "jellyfin";
type PathReplacementPair = { id: string; source: string; local: string };

let nextPathReplacementID = 1;

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

export function MediaSourcesPanel() {
  const [plex, setPlex] = useState<PlexStatus>({ connected: false });
  const [jellyfin, setJellyfin] = useState<JellyfinStatus>({ connected: false, configured: false });
  const [localSources, setLocalSources] = useState<LocalMediaSource[]>([]);
  const [editingLocal, setEditingLocal] = useState<LocalMediaSource | null>(null);
  const [loaded, setLoaded] = useState(false);
  const [dialog, setDialog] = useState<DialogKind>("none");
  const [scan, setScan] = useState<ScanKind>("none");
  const [error, setError] = useState("");

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

  return (
    <section className="admin-panel-section" aria-label="Media sources">
      <div className="section-headline">
        <h2>Media sources</h2>
        <button type="button" disabled={!loaded} onClick={() => setDialog("picker")}>
          + Add media source
        </button>
      </div>

      {plex.connected && (
        <PlexCard
          status={plex}
          onReplace={() => setDialog("plex")}
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
            onClick={() => setDialog("plex")}
            title={plex.connected ? "Plex already connected" : undefined}
          >
            <span className={styles["plex-logo"]} aria-hidden="true">P</span>
            <span>Plex (Manual){plex.connected ? " (connected)" : ""}</span>
          </button>
          <button
            type="button"
            className={styles["kind-option"]}
            disabled={jellyfin.connected}
            onClick={() => setDialog("jellyfin")}
            title={jellyfin.connected ? "Jellyfin already connected" : undefined}
          >
            <span className="plex-logo jellyfin-logo" aria-hidden="true">J</span>
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
        onClose={closeDialog}
        onSaved={(next) => {
          setPlex(next);
          setDialog("none");
        }}
      />
      <JellyfinFormDialog
        open={dialog === "jellyfin"}
        initial={jellyfin}
        onClose={closeDialog}
        onSaved={(next) => {
          setJellyfin(next);
          setDialog("none");
        }}
      />
      <LocalFormDialog
        open={dialog === "local"}
        initial={editingLocal}
        onClose={closeDialog}
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
        <strong>Plex (Manual)</strong>
        <span className={styles["plex-connected-pill"]}>Connected</span>
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
        <span className="plex-logo jellyfin-logo" aria-hidden="true">J</span>
        <strong>Jellyfin</strong>
        <span className={styles["plex-connected-pill"]}>Connected</span>
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

function LocalCard({ source, onReplace, onDelete }: LocalCardProps) {
  const scan = useScanJob();

  async function startScan() {
    await scan.start(() => startLocalMediaSourceScan(source.id));
  }

  const [primaryPath, ...extraPaths] = source.paths;
  const running = scan.job?.status === "running";

  return (
    <div className={styles["media-server-section"]}>
      <div className={styles["media-server-connected-row"]}>
        <strong>Local</strong>
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
      <ScanResult job={scan.job} error={scan.startError} />
    </div>
  );
}

// useScanJob owns the poll/cancel lifecycle for one source's ingest job. Used
// by both LocalCard and ScanPanel so they share polling, cancel, and result
// rendering instead of forking the state machine in two places.
function useScanJob() {
  const [jobId, setJobId] = useState<string | null>(null);
  const [job, setJob] = useState<IngestJob | null>(null);
  const [startError, setStartError] = useState("");

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
        if (next.status !== "running") return "stop";
      } catch (err) {
        if (signal.aborted) return "stop";
        throw err;
      }
    },
  });

  async function start(launch: () => Promise<{ jobId: string }>) {
    setStartError("");
    setJob({ status: "running" });
    try {
      const { jobId: id } = await launch();
      setJobId(id);
    } catch (err) {
      setJob(null);
      setJobId(null);
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

  return { job, startError, start, cancel };
}

function ScanResult({ job, error }: { job: IngestJob | null; error: string }) {
  if (error) {
    return <div className={styles["ingest-log"]}><div className={styles["ingest-log-summary"]}><span className="danger">{error}</span></div></div>;
  }
  if (!job) return null;
  if (job.status === "running") {
    if (job.total == null || job.processed == null) return null;
    return (
      <div className={styles["ingest-log"]}>
        <div className={styles["ingest-log-summary"]}>
          <span className="muted">
            <span className={styles["scan-spinner"]} aria-hidden="true" /> {job.processed} / {job.total}
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

type PlexFormDialogProps = {
  open: boolean;
  initial: PlexStatus;
  onClose: () => void;
  onSaved: (next: PlexStatus) => void;
};

function PlexFormDialog({ open, initial, onClose, onSaved }: PlexFormDialogProps) {
  const [url, setURL] = useState(initial.url || "");
  const [token, setToken] = useState("");
  const [pathPairs, setPathPairs] = useState(() => parsePathReplacementPairs(initial.connected ? initial.pathMap || "" : ""));
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");

  useEffect(() => {
    if (!open) return;
    setURL(initial.url || "");
    setToken("");
    setPathPairs(parsePathReplacementPairs(initial.connected ? initial.pathMap || "" : ""));
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
      const next = await setPlexConfig(url, token, serializePathReplacementPairs(pathPairs));
      onSaved(next);
    } catch (err) {
      setMessage(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog open={open} onClose={onClose} title={initial.connected ? "Replace Plex (Manual) connection" : "Add Plex (Manual)"}>
      <form className={styles["dialog-form"]} onSubmit={(event) => void submit(event)}>
        <label>
          <span>URL</span>
          <input
            type="url"
            value={url}
            disabled={busy}
            placeholder="http://plex:32400"
            onChange={(e) => setURL(e.target.value)}
            required
          />
        </label>
        <label>
          <span>X-Plex-Token</span>
          <input
            type="password"
            value={token}
            disabled={busy}
            onChange={(e) => setToken(e.target.value)}
            required
          />
        </label>
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
        <a
          href="https://support.plex.tv/articles/204059436-finding-an-authentication-token-x-plex-token/"
          target="_blank"
          rel="noreferrer"
          className={styles["media-server-help-link"]}
        >
          How to find your token
        </a>
        {message && <div className="plex-token-error">{message}</div>}
        <div className={styles["dialog-actions"]}>
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
};

function LocalFormDialog({ open, initial, onClose, onSaved }: LocalFormDialogProps) {
  const [rootDirs, setRootDirs] = useState<{ name: string; path: string; mediaCount: number }[]>([]);
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
    browseFs()
      .then((r) => setRootDirs(r.dirs))
      .catch(() => setRootDirs([]));
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

  function addSuggestedPath(path: string) {
    const nextPath = path.trim();
    if (!nextPath) return;
    setPaths((prev) => {
      const existing = prev.map((item) => item.trim()).filter(Boolean);
      if (existing.includes(nextPath)) return existing.length ? existing : [nextPath];
      return [...existing, nextPath];
    });
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
        </label>
        {rootDirs.length > 0 && (
          <div className={styles["filesystem-suggestions"]}>
            <div className={styles["dialog-field-heading"]}>Filesystem suggestions</div>
            <p className="muted">Directories visible to this server. Select one to add it to Paths.</p>
            <div className={styles["ingest-root-dirs"]}>
              {rootDirs.map((d) => (
                <button
                  key={d.path}
                  type="button"
                  className={styles["ingest-dir-chip"]}
                  disabled={busy}
                  onClick={() => addSuggestedPath(d.path)}
                  title={d.path}
                >
                  <span className={styles["ingest-dir-chip-name"]}>{d.name}</span>
                  <span className={styles["ingest-dir-chip-path"]}>{d.path}</span>
                  {d.mediaCount > 0 && <span className={styles["ingest-dir-chip-count"]}>{d.mediaCount} media files</span>}
                </button>
              ))}
            </div>
          </div>
        )}
        <label>
          <span>Paths</span>
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
                <button type="button" className="link-button" disabled={busy} onClick={() => removePath(index)}>
                  Remove
                </button>
              </div>
            ))}
          </div>
        </label>
        <button type="button" className="link-button" disabled={busy} onClick={() => addPath()}>
          + Add path
        </button>
        {message && <div className="plex-token-error">{message}</div>}
        <div className={styles["dialog-actions"]}>
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
};

function JellyfinFormDialog({ open, initial, onClose, onSaved }: JellyfinFormDialogProps) {
  const [url, setURL] = useState(initial.url || "");
  const [apiKey, setAPIKey] = useState("");
  const [pathPairs, setPathPairs] = useState(() => parsePathReplacementPairs(initial.connected ? initial.pathMap || "" : ""));
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState("");

  useEffect(() => {
    if (!open) return;
    setURL(initial.url || "");
    setAPIKey("");
    setPathPairs(parsePathReplacementPairs(initial.connected ? initial.pathMap || "" : ""));
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
        {message && <div className="plex-token-error">{message}</div>}
        <div className={styles["dialog-actions"]}>
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

  const scan = useScanJob();
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
      <ScanResult job={scan.job} error={scan.startError} />
    </div>
  );
}
