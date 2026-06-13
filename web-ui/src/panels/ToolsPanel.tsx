import { FormEvent, useEffect, useState } from "react";
import {
  cleanupInvalidProfilePackages,
  getCacheSummary,
  getEncoderSweeperSettings,
  getOnDemandSessionSettings,
  getSchedulerTunables,
  getSubtitleSettings,
  optimizeDatabase,
  updateEncoderSweeperSettings,
  updateOnDemandSessionSettings,
  updateSchedulerTunables,
  updateSubtitleSettings,
} from "../api";
import { formatBytes, formatMs } from "../format";
import { MediaSourcesPanel } from "./MediaSourcesPanel";
import { MaintenancePanel } from "./MaintenancePanel";
import { AddLiveChannelDialog } from "./AddLiveChannelDialog";
import { WriteLogPanel } from "./WriteLogPanel";
import type { CacheSummary } from "../types";
import styles from "./ToolsPanel.module.css";

export function ToolsPanel({ onChannelImported }: { onChannelImported: () => void }) {
  const [cacheSummary, setCacheSummary] = useState<CacheSummary | null>(null);
  const [cacheBusy, setCacheBusy] = useState(false);
  const [cacheStatus, setCacheStatus] = useState("");
  const [addLiveOpen, setAddLiveOpen] = useState(false);

  async function refreshCache() {
    setCacheBusy(true);
    setCacheStatus(cacheSummary ? "refreshing…" : "loading…");
    try {
      setCacheSummary(await getCacheSummary());
      setCacheStatus("");
    } catch (err) {
      setCacheStatus(err instanceof Error ? err.message : String(err));
    } finally {
      setCacheBusy(false);
    }
  }

  useEffect(() => {
    void refreshCache();
  }, []);

  return (
    <>
    <div className="admin-panel">
      <MediaSourcesPanel />

      <section className="admin-panel-section">
        <div className="section-headline">
          <h2>Live channels</h2>
          <button type="button" onClick={() => setAddLiveOpen(true)}>
            + Add live channel
          </button>
        </div>
        <p className="muted">
          Create a channel that proxies an upstream HLS manifest. It goes live
          immediately and appears in the guide on linearcast's next ~60s refresh.
        </p>
      </section>

      <section className="admin-panel-section">
        <div className="section-headline">
          <h2>Cache monitoring</h2>
          <button type="button" disabled={cacheBusy} onClick={() => void refreshCache()}>
            {cacheBusy ? "refreshing" : "refresh"}
          </button>
        </div>
        <CacheSummaryPanel
          summary={cacheSummary}
          status={cacheStatus}
          onCleanupInvalidProfiles={async () => {
            const preview = await cleanupInvalidProfilePackages(true);
            if (preview.removed.length === 0) return;
            const msg = `Remove ${preview.removed.length} invalid-profile package(s) (${formatBytes(preview.totalBytes)} on disk)?`;
            if (!window.confirm(msg)) return;
            await cleanupInvalidProfilePackages(false);
            void refreshCache();
          }}
        />
      </section>

      <MaintenancePanel onChanged={() => void refreshCache()} />

      <section className={`admin-panel-section ${styles["settings-row"]}`}>
        <SchedulerTunablesPanel />
        <EncoderSweeperSettingsPanel />
        <OnDemandSessionSettingsPanel />
        <SubtitleSettingsPanel />
      </section>
    </div>

    <WriteLogPanel />

    <AddLiveChannelDialog
      open={addLiveOpen}
      onClose={() => setAddLiveOpen(false)}
      onCreated={() => {
        setAddLiveOpen(false);
        onChannelImported();
      }}
    />
    </>
  );
}

function SchedulerTunablesPanel() {
  const [draft, setDraft] = useState({ horizonHours: "", lowWaterHours: "", tickSeconds: "" });
  const [loaded, setLoaded] = useState(false);
  const [busy, setBusy] = useState(false);
  const [status, setStatus] = useState("");

  useEffect(() => {
    let cancelled = false;
    getSchedulerTunables()
      .then((t) => {
        if (cancelled) return;
        setDraft({
          horizonHours: String(t.horizonHours),
          lowWaterHours: String(t.lowWaterHours),
          tickSeconds: String(t.tickSeconds),
        });
        setLoaded(true);
        setStatus("");
      })
      .catch((err) => {
        if (!cancelled) setStatus(err instanceof Error ? err.message : String(err));
      });
    return () => {
      cancelled = true;
    };
  }, []);

  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const horizonHours = parseInt(draft.horizonHours, 10);
    const lowWaterHours = parseInt(draft.lowWaterHours, 10);
    const tickSeconds = parseInt(draft.tickSeconds, 10);

    if (Number.isNaN(horizonHours) || horizonHours <= 0) {
      setStatus("horizon hours must be > 0");
      return;
    }
    if (Number.isNaN(lowWaterHours) || lowWaterHours <= 0) {
      setStatus("low-water hours must be > 0");
      return;
    }
    if (Number.isNaN(tickSeconds) || tickSeconds <= 0) {
      setStatus("tick seconds must be > 0");
      return;
    }
    if (lowWaterHours >= horizonHours) {
      setStatus("low-water hours must be less than horizon hours");
      return;
    }

    const payload = { horizonHours, lowWaterHours, tickSeconds };
    setBusy(true);
    setStatus("saving...");
    try {
      const saved = await updateSchedulerTunables(payload);
      setDraft({
        horizonHours: String(saved.horizonHours),
        lowWaterHours: String(saved.lowWaterHours),
        tickSeconds: String(saved.tickSeconds),
      });
      setStatus("saved");
    } catch (err) {
      setStatus(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles["settings-col"]}>
      <h2>Scheduler tunables</h2>
      <form className={`${styles["admin-form"]} scheduler-tunables-form`} onSubmit={(event) => void save(event)}>
        <label>
          <span>horizon hours</span>
          <input
            type="number"
            min={1}
            value={draft.horizonHours}
            disabled={busy || !loaded}
            onChange={(event) =>
              setDraft((prev) => ({ ...prev, horizonHours: event.target.value }))
            }
          />
        </label>
        <label>
          <span>low-water hours</span>
          <input
            type="number"
            min={1}
            value={draft.lowWaterHours}
            disabled={busy || !loaded}
            onChange={(event) =>
              setDraft((prev) => ({ ...prev, lowWaterHours: event.target.value }))
            }
          />
        </label>
        <label>
          <span>tick seconds</span>
          <input
            type="number"
            min={1}
            value={draft.tickSeconds}
            disabled={busy || !loaded}
            onChange={(event) =>
              setDraft((prev) => ({ ...prev, tickSeconds: event.target.value }))
            }
          />
        </label>
        <div className="admin-form-actions">
          <button type="submit" disabled={busy || !loaded}>
            {busy ? "Saving..." : "Save tunables"}
          </button>
          {status && <span className="muted">{status}</span>}
        </div>
      </form>
    </div>
  );
}

function EncoderSweeperSettingsPanel() {
  const [draft, setDraft] = useState({ sweepIntervalSeconds: "", maxAttempts: "" });
  const [loaded, setLoaded] = useState(false);
  const [busy, setBusy] = useState(false);
  const [status, setStatus] = useState("");

  useEffect(() => {
    let cancelled = false;
    getEncoderSweeperSettings()
      .then((s) => {
        if (cancelled) return;
        setDraft({
          sweepIntervalSeconds: String(s.sweepIntervalSeconds),
          maxAttempts: String(s.maxAttempts),
        });
        setLoaded(true);
        setStatus("");
      })
      .catch((err) => {
        if (!cancelled) setStatus(err instanceof Error ? err.message : String(err));
      });
    return () => {
      cancelled = true;
    };
  }, []);

  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const sweepIntervalSeconds = parseInt(draft.sweepIntervalSeconds, 10);
    const maxAttempts = parseInt(draft.maxAttempts, 10);

    if (Number.isNaN(sweepIntervalSeconds) || sweepIntervalSeconds <= 0) {
      setStatus("sweep interval seconds must be > 0");
      return;
    }
    if (Number.isNaN(maxAttempts) || maxAttempts <= 0) {
      setStatus("max attempts must be > 0");
      return;
    }

    const payload = { sweepIntervalSeconds, maxAttempts };
    setBusy(true);
    setStatus("saving...");
    try {
      const saved = await updateEncoderSweeperSettings(payload);
      setDraft({
        sweepIntervalSeconds: String(saved.sweepIntervalSeconds),
        maxAttempts: String(saved.maxAttempts),
      });
      setStatus("saved");
    } catch (err) {
      setStatus(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles["settings-col"]}>
      <h2>Encoder sweeper</h2>
      <form className={`${styles["admin-form"]} encoder-sweeper-form`} onSubmit={(event) => void save(event)}>
        <label>
          <span>sweep interval (seconds)</span>
          <input
            type="number"
            min={1}
            value={draft.sweepIntervalSeconds}
            disabled={busy || !loaded}
            onChange={(event) =>
              setDraft((prev) => ({ ...prev, sweepIntervalSeconds: event.target.value }))
            }
          />
        </label>
        <label>
          <span>max attempts</span>
          <input
            type="number"
            min={1}
            value={draft.maxAttempts}
            disabled={busy || !loaded}
            onChange={(event) =>
              setDraft((prev) => ({ ...prev, maxAttempts: event.target.value }))
            }
          />
        </label>
        <div className="admin-form-actions">
          <button type="submit" disabled={busy || !loaded}>
            {busy ? "Saving..." : "Save sweeper settings"}
          </button>
          {status && <span className="muted">{status}</span>}
        </div>
      </form>
    </div>
  );
}

function SubtitleSettingsPanel() {
  // Only controls auto-enable. Language preference lives in the Subtitles panel.
  // We still fetch + round-trip the language preference so saving auto-enable
  // doesn't clobber it.
  const [autoEnable, setAutoEnable] = useState(false);
  const [languagePreference, setLanguagePreference] = useState<string[]>(["eng"]);
  const [loaded, setLoaded] = useState(false);
  const [busy, setBusy] = useState(false);
  const [status, setStatus] = useState("");

  useEffect(() => {
    let cancelled = false;
    getSubtitleSettings()
      .then((settings) => {
        if (cancelled) return;
        setAutoEnable(settings.subtitleAutoEnable);
        setLanguagePreference(settings.subtitleLanguagePreference);
        setLoaded(true);
      })
      .catch((err) => {
        if (!cancelled) setStatus(err instanceof Error ? err.message : String(err));
      });
    return () => { cancelled = true; };
  }, []);

  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setBusy(true);
    setStatus("saving...");
    try {
      const saved = await updateSubtitleSettings({
        subtitleAutoEnable: autoEnable,
        subtitleLanguagePreference: languagePreference,
      });
      setAutoEnable(saved.subtitleAutoEnable);
      setLanguagePreference(saved.subtitleLanguagePreference);
      setStatus("saved");
    } catch (err) {
      setStatus(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles["settings-col"]}>
      <h2>Subtitle settings</h2>
      <form className={`${styles["admin-form"]} ${styles["subtitle-settings-form"]}`} onSubmit={(event) => void save(event)}>
        <label className={styles["admin-checkbox-label"]}>
          <input
            type="checkbox"
            checked={autoEnable}
            disabled={busy || !loaded}
            onChange={(event) => setAutoEnable(event.target.checked)}
          />
          <span>auto-enable top language in player</span>
        </label>
        <div className="admin-form-actions">
          <button type="submit" disabled={busy || !loaded}>
            {busy ? "Saving..." : "Save"}
          </button>
          {status && <span className="muted">{status}</span>}
        </div>
      </form>
    </div>
  );
}

function OnDemandSessionSettingsPanel() {
  const [draft, setDraft] = useState({
    graceSeconds: "",
    maxConcurrent: "",
    evictIdleSeconds: "",
    stallTimeoutSeconds: "",
    restartBudget: "",
    keepaliveCeilingSec: "",
  });
  const [loaded, setLoaded] = useState(false);
  const [busy, setBusy] = useState(false);
  const [status, setStatus] = useState("");

  useEffect(() => {
    let cancelled = false;
    getOnDemandSessionSettings()
      .then((s) => {
        if (cancelled) return;
        setDraft({
          graceSeconds: String(s.graceSeconds),
          maxConcurrent: String(s.maxConcurrent),
          evictIdleSeconds: String(s.evictIdleSeconds),
          stallTimeoutSeconds: String(s.stallTimeoutSeconds),
          restartBudget: String(s.restartBudget),
          keepaliveCeilingSec: String(s.keepaliveCeilingSec),
        });
        setLoaded(true);
        setStatus("");
      })
      .catch((err) => {
        if (!cancelled) setStatus(err instanceof Error ? err.message : String(err));
      });
    return () => { cancelled = true; };
  }, []);

  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const graceSeconds = parseInt(draft.graceSeconds, 10);
    const maxConcurrent = parseInt(draft.maxConcurrent, 10);
    const evictIdleSeconds = parseInt(draft.evictIdleSeconds, 10);
    const stallTimeoutSeconds = parseInt(draft.stallTimeoutSeconds, 10);
    const restartBudget = parseInt(draft.restartBudget, 10);
    const keepaliveCeilingSec = parseInt(draft.keepaliveCeilingSec, 10);

    if (Number.isNaN(graceSeconds) || graceSeconds <= 0) {
      setStatus("grace seconds must be > 0");
      return;
    }
    if (Number.isNaN(maxConcurrent) || maxConcurrent <= 0) {
      setStatus("max concurrent must be > 0");
      return;
    }
    if (Number.isNaN(evictIdleSeconds) || evictIdleSeconds <= 0) {
      setStatus("evict idle seconds must be > 0");
      return;
    }
    if (Number.isNaN(stallTimeoutSeconds) || stallTimeoutSeconds <= 0) {
      setStatus("stall timeout seconds must be > 0");
      return;
    }
    if (Number.isNaN(restartBudget) || restartBudget <= 0) {
      setStatus("restart budget must be > 0");
      return;
    }
    if (Number.isNaN(keepaliveCeilingSec) || keepaliveCeilingSec <= 0) {
      setStatus("keepalive ceiling must be > 0");
      return;
    }

    const payload = { graceSeconds, maxConcurrent, evictIdleSeconds, stallTimeoutSeconds, restartBudget, keepaliveCeilingSec };
    setBusy(true);
    setStatus("saving...");
    try {
      const saved = await updateOnDemandSessionSettings(payload);
      setDraft({
        graceSeconds: String(saved.graceSeconds),
        maxConcurrent: String(saved.maxConcurrent),
        evictIdleSeconds: String(saved.evictIdleSeconds),
        stallTimeoutSeconds: String(saved.stallTimeoutSeconds),
        restartBudget: String(saved.restartBudget),
        keepaliveCeilingSec: String(saved.keepaliveCeilingSec),
      });
      setStatus("saved");
    } catch (err) {
      setStatus(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className={styles["settings-col"]}>
      <h2>On-demand session</h2>
      <form className={`${styles["admin-form"]} on-demand-session-form`} onSubmit={(event) => void save(event)}>
        <label>
          <span>grace (seconds)</span>
          <input
            type="number"
            min={1}
            value={draft.graceSeconds}
            disabled={busy || !loaded}
            onChange={(event) =>
              setDraft((prev) => ({ ...prev, graceSeconds: event.target.value }))
            }
          />
        </label>
        <label>
          <span>max concurrent</span>
          <input
            type="number"
            min={1}
            value={draft.maxConcurrent}
            disabled={busy || !loaded}
            onChange={(event) =>
              setDraft((prev) => ({ ...prev, maxConcurrent: event.target.value }))
            }
          />
        </label>
        <label>
          <span>evict idle (seconds)</span>
          <input
            type="number"
            min={1}
            value={draft.evictIdleSeconds}
            disabled={busy || !loaded}
            onChange={(event) =>
              setDraft((prev) => ({ ...prev, evictIdleSeconds: event.target.value }))
            }
          />
        </label>
        <label>
          <span>stall timeout (seconds)</span>
          <input
            type="number"
            min={1}
            value={draft.stallTimeoutSeconds}
            disabled={busy || !loaded}
            onChange={(event) =>
              setDraft((prev) => ({ ...prev, stallTimeoutSeconds: event.target.value }))
            }
          />
        </label>
        <label>
          <span>restart budget</span>
          <input
            type="number"
            min={1}
            value={draft.restartBudget}
            disabled={busy || !loaded}
            onChange={(event) =>
              setDraft((prev) => ({ ...prev, restartBudget: event.target.value }))
            }
          />
        </label>
        <label>
          <span>keepalive ceiling (sec)</span>
          <input
            type="number"
            min={1}
            value={draft.keepaliveCeilingSec}
            disabled={busy || !loaded}
            onChange={(event) =>
              setDraft((prev) => ({ ...prev, keepaliveCeilingSec: event.target.value }))
            }
          />
        </label>
        <div className="admin-form-actions">
          <button type="submit" disabled={busy || !loaded}>
            {busy ? "Saving..." : "Save session settings"}
          </button>
          {status && <span className="muted">{status}</span>}
        </div>
      </form>
    </div>
  );
}

function CacheSummaryPanel({
  summary,
  status,
  onCleanupInvalidProfiles,
}: {
  summary: CacheSummary | null;
  status: string;
  onCleanupInvalidProfiles?: () => Promise<void>;
}) {
  const [cleanupBusy, setCleanupBusy] = useState(false);
  const [cleanupStatus, setCleanupStatus] = useState("");
  if (!summary) {
    return <span className="muted">{status || "loading cache summary"}</span>;
  }

  const readyRows = summary.channelSummaries
    .filter((row) => row.status === "ready")
    .slice()
    .sort((a, b) => b.readyDurationMs - a.readyDurationMs);
  const readyByChannel = new Map(readyRows.map((row) => [`${row.channelId}:${row.renditionProfile}`, row]));
  const needRows = (summary.channelNeeds ?? []).slice().sort((a, b) => b.remainingCount - a.remainingCount);

  // Collapse per-status rows into one entry per profile, skipping pending.
  const profileMap = new Map<string, { ready: number; processing: number; bytes: number; durationMs: number; invalid: boolean; disabled: boolean }>();
  for (const row of summary.packageSummaries ?? []) {
    if (row.status === "pending") continue;
    const key = row.renditionProfile;
    const existing = profileMap.get(key) ?? { ready: 0, processing: 0, bytes: 0, durationMs: 0, invalid: false, disabled: false };
    if (row.status === "ready") {
      existing.ready += row.packageCount;
      existing.bytes += row.packageBytes;
      existing.durationMs += row.readyDurationMs;
    } else if (row.status === "processing") {
      existing.processing += row.packageCount;
      existing.bytes += row.packageBytes;
    }
    existing.invalid = existing.invalid || !!row.invalid;
    existing.disabled = existing.disabled || !!row.disabled;
    profileMap.set(key, existing);
  }
  const profileRows = Array.from(profileMap.entries()).sort(([a], [b]) => a.localeCompare(b));

  return (
    <div className={styles["cache-summary"]}>
      <div className={styles["cache-summary-grid"]}>
        <span>cache</span>
        <strong>{formatBytes(summary.cacheRootBytes)}</strong>
        <span>packages</span>
        <strong>{formatBytes(summary.packageRootBytes ?? summary.packageBytes)}</strong>
        <span>roots</span>
        <strong>{summary.packageRootCount}</strong>
      </div>

      {profileRows.length > 0 && (
        <ul className={styles["cache-channel-list"]}>
          {profileRows.map(([profile, agg]) => {
            const details = [
              agg.ready > 0 ? `${agg.ready} ready` : "",
              agg.processing > 0 ? `${agg.processing} encoding` : "",
              agg.disabled ? "disabled" : "",
              agg.bytes > 0 ? formatBytes(agg.bytes) : "",
              agg.durationMs > 0 ? formatMs(agg.durationMs) : "",
            ].filter(Boolean);
            return (
              <li key={profile}>
                <span className={agg.invalid ? "danger" : ""}>{profile}</span>
                <span>{details.join(" · ")}</span>
              </li>
            );
          })}
        </ul>
      )}
      {(summary.packageSummaries ?? []).some((r) => r.invalid) && onCleanupInvalidProfiles && (
        <div className="cache-cleanup-row">
          <button
            type="button"
            className="danger"
            disabled={cleanupBusy}
            onClick={() => {
              setCleanupBusy(true);
              setCleanupStatus("");
              onCleanupInvalidProfiles()
                .then(() => setCleanupStatus("cleaned up"))
                .catch((err: unknown) => setCleanupStatus(err instanceof Error ? err.message : String(err)))
                .finally(() => setCleanupBusy(false));
            }}
          >
            {cleanupBusy ? "cleaning…" : "Clean up invalid profiles"}
          </button>
          {cleanupStatus && <span className="muted">{cleanupStatus}</span>}
        </div>
      )}
      {needRows.length > 0 && (
        <ul className={styles["cache-channel-list"]}>
          {needRows.map((row) => {
            const ready = readyByChannel.get(`${row.channelId}:${row.renditionProfile}`);
            const details = [
              `${row.readyCount}/${row.neededCount} ready`,
              row.processingCount > 0 ? `${row.processingCount} encoding` : "",
              row.failedCount > 0 ? `${row.failedCount} failed` : "",
              row.remainingCount > 0 ? `${row.remainingCount} remaining` : "",
              ready ? formatBytes(ready.packageBytes) : "",
              ready ? formatMs(ready.readyDurationMs) : "",
            ].filter(Boolean);
            return (
              <li key={`${row.channelId}:${row.renditionProfile}`}>
                <span>{row.displayName || row.channelId}</span>
                <span>{details.join(" · ")}</span>
              </li>
            );
          })}
        </ul>
      )}
      {summary.warnings?.map((warning) => (
        <span key={warning} className={`muted ${styles["cache-warning"]}`}>
          {warning}
        </span>
      ))}
      {status && <span className={`muted ${styles["cache-warning"]}`}>{status}</span>}
    </div>
  );
}
