import { FormEvent, ReactNode, useEffect, useState } from "react";
import {
  cleanupInvalidProfilePackages,
  clearSpotifyUrl,
  describeProbeResult,
  getCacheSummary,
  getEncoderSweeperSettings,
  getPublicServerURL,
  getSchedulerTunables,
  getSpotifyUrl,
  getSubtitleSettings,
  probeUpstreamHLS,
  saveSpotifyUrl,
  updateEncoderSweeperSettings,
  updatePublicServerURL,
  updateSchedulerTunables,
  updateSubtitleSettings,
} from "../api";
import { formatBytes, formatMs } from "../format";
import type { CacheSummary, SpotifyUrl } from "../types";
import { MaintenancePanel } from "./MaintenancePanel";
import { WriteLogPanel } from "./WriteLogPanel";
import styles from "./ToolsPanel.module.css";

export function ToolsPanel({ onChannelChanged }: { onChannelChanged?: () => void }) {
  const [cacheSummary, setCacheSummary] = useState<CacheSummary | null>(null);
  const [cacheBusy, setCacheBusy] = useState(false);
  const [cacheStatus, setCacheStatus] = useState("");

  async function refreshCache() {
    setCacheBusy(true);
    setCacheStatus(cacheSummary ? "refreshing..." : "loading...");
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
        <SettingsSection onChannelChanged={onChannelChanged} />

        <section className="admin-panel-section">
          <div className="section-headline">
            <div className="section-headline-main">
              <h2>Cache monitoring</h2>
              <p className="section-purpose">
                Packaged-media disk usage and readiness, broken down by channel and profile.
              </p>
            </div>
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

        <MaintenancePanel onChanged={() => {}} />
      </div>

      <WriteLogPanel />
    </>
  );
}

// ---------------------------------------------------------------------------
// Settings — one table consolidating the scalar operational tunables that used
// to live in their own per-group sections (the scheduler tunables that lived
// under Guide, the encoder sweeper, subtitles) plus the singleton Spotify→HLS
// channel URL. The single top-right Save persists every underlying endpoint.
// ---------------------------------------------------------------------------

const SETTINGS_HELP = {
  horizonHours: "How far ahead the scheduler tries to keep each channel filled. Larger values build more schedule in advance.",
  lowWaterHours: "When remaining coverage drops below this many hours, the scheduler extends the channel back up toward the horizon. Must be less than horizon hours.",
  tickSeconds: "How often the scheduler wakes up to check whether any channel has fallen below the low-water mark.",
  sweepIntervalSeconds: "How often the admin server checks for encoder jobs whose lease expired because the worker stopped heartbeating.",
  maxAttempts: "How many transient encode failures a package can accumulate before it is marked failed instead of being retried.",
  subtitleLanguages: "Preferred subtitle languages, in order, used when extracting and selecting caption tracks. Comma-separated 3-letter ISO 639-2 codes, e.g. eng, spa.",
  subtitleAutoEnable: "Automatically enable the top preferred language as a caption track in the player.",
  spotifyUrl: "Play one external audio stream — a Spotify→HLS bridge — as a channel. Set or update the HLS URL to upsert the channel; clear it to remove the channel. Appears in the guide within a minute.",
  publicServerUrl: "Public origin used for copyable IPTV/DVR URLs. Leave empty to use the address currently open in this browser.",
};

type SettingsDraft = {
  horizonHours: string;
  lowWaterHours: string;
  tickSeconds: string;
  sweepIntervalSeconds: string;
  maxAttempts: string;
  subtitleLanguages: string;
  subtitleAutoEnable: boolean;
  spotifyUrl: string;
  publicServerUrl: string;
};

type NumericSettingKey = "horizonHours" | "lowWaterHours" | "tickSeconds" | "sweepIntervalSeconds" | "maxAttempts";

const EMPTY_SETTINGS_DRAFT: SettingsDraft = {
  horizonHours: "",
  lowWaterHours: "",
  tickSeconds: "",
  sweepIntervalSeconds: "",
  maxAttempts: "",
  subtitleLanguages: "",
  subtitleAutoEnable: false,
  spotifyUrl: "",
  publicServerUrl: "",
};

export function SettingsSection({ onChannelChanged }: { onChannelChanged?: () => void }) {
  const [draft, setDraft] = useState<SettingsDraft>(EMPTY_SETTINGS_DRAFT);
  const [spotify, setSpotify] = useState<SpotifyUrl | null>(null);
  const [probe, setProbe] = useState<{ ok: boolean; text: string } | null>(null);
  const [probing, setProbing] = useState(false);
  const [loaded, setLoaded] = useState(false);
  const [busy, setBusy] = useState(false);
  const [status, setStatus] = useState("");

  useEffect(() => {
    let cancelled = false;
    Promise.all([getSchedulerTunables(), getEncoderSweeperSettings(), getSubtitleSettings(), getSpotifyUrl(), getPublicServerURL()])
      .then(([scheduler, sweeper, subtitles, spotifyUrl, publicServer]) => {
        if (cancelled) return;
        setSpotify(spotifyUrl);
        setDraft({
          horizonHours: String(scheduler.horizonHours),
          lowWaterHours: String(scheduler.lowWaterHours),
          tickSeconds: String(scheduler.tickSeconds),
          sweepIntervalSeconds: String(sweeper.sweepIntervalSeconds),
          maxAttempts: String(sweeper.maxAttempts),
          subtitleLanguages: subtitles.subtitleLanguagePreference.join(", "),
          subtitleAutoEnable: subtitles.subtitleAutoEnable,
          spotifyUrl: spotifyUrl.upstreamHlsUrl ?? "",
          publicServerUrl: publicServer.publicServerUrl ?? "",
        });
        setLoaded(true);
        setStatus("");
      })
      .catch((err) => {
        if (!cancelled) setStatus(err instanceof Error ? err.message : String(err));
      });
    return () => { cancelled = true; };
  }, []);

  function setField(key: keyof SettingsDraft, value: string | boolean) {
    setDraft((prev) => ({ ...prev, [key]: value }));
    setStatus("");
  }

  async function testSpotify() {
    const trimmed = draft.spotifyUrl.trim();
    if (!trimmed) return;
    setProbing(true);
    setProbe(null);
    try {
      setProbe(describeProbeResult(await probeUpstreamHLS(trimmed)));
    } catch (err) {
      setProbe({ ok: false, text: err instanceof Error ? err.message : String(err) });
    } finally {
      setProbing(false);
    }
  }

  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();

    const horizonHours = parseInt(draft.horizonHours, 10);
    const lowWaterHours = parseInt(draft.lowWaterHours, 10);
    const tickSeconds = parseInt(draft.tickSeconds, 10);
    const sweepIntervalSeconds = parseInt(draft.sweepIntervalSeconds, 10);
    const maxAttempts = parseInt(draft.maxAttempts, 10);

    const positive: [string, number][] = [
      ["Horizon hours", horizonHours],
      ["Low-water hours", lowWaterHours],
      ["Tick seconds", tickSeconds],
      ["Sweep interval (seconds)", sweepIntervalSeconds],
      ["Max attempts", maxAttempts],
    ];
    for (const [label, value] of positive) {
      if (Number.isNaN(value) || value <= 0) {
        setStatus(`${label} must be greater than 0`);
        return;
      }
    }
    if (lowWaterHours >= horizonHours) {
      setStatus("Low-water hours must be less than horizon hours");
      return;
    }
    const langs = parseSubtitleLanguageText(draft.subtitleLanguages);
    if (!langs.valid) {
      setStatus(langs.message);
      return;
    }

    // Spotify URL is upserted when set/changed and the channel is removed when
    // the field is emptied — guard the destructive case before anything saves.
    const spotifyTrimmed = draft.spotifyUrl.trim();
    const spotifyOriginal = spotify?.upstreamHlsUrl ?? "";
    const spotifyChanged = spotifyTrimmed !== spotifyOriginal;
    const clearingSpotify = spotifyChanged && spotifyTrimmed === "" && (spotify?.configured ?? false);
    if (clearingSpotify && !window.confirm("Remove the Spotify channel?")) return;

    setBusy(true);
    setStatus("saving…");
    try {
      const [scheduler, sweeper, subtitles, publicServer] = await Promise.all([
        updateSchedulerTunables({ horizonHours, lowWaterHours, tickSeconds }),
        updateEncoderSweeperSettings({ sweepIntervalSeconds, maxAttempts }),
        updateSubtitleSettings({
          subtitleAutoEnable: draft.subtitleAutoEnable,
          subtitleLanguagePreference: langs.languages,
        }),
        updatePublicServerURL(draft.publicServerUrl.trim()),
      ]);
      let nextSpotify = spotify;
      if (spotifyChanged) {
        nextSpotify = spotifyTrimmed ? await saveSpotifyUrl(spotifyTrimmed) : await clearSpotifyUrl();
        setSpotify(nextSpotify);
        setProbe(null);
      }
      setDraft({
        horizonHours: String(scheduler.horizonHours),
        lowWaterHours: String(scheduler.lowWaterHours),
        tickSeconds: String(scheduler.tickSeconds),
        sweepIntervalSeconds: String(sweeper.sweepIntervalSeconds),
        maxAttempts: String(sweeper.maxAttempts),
        subtitleLanguages: subtitles.subtitleLanguagePreference.join(", "),
        subtitleAutoEnable: subtitles.subtitleAutoEnable,
        spotifyUrl: nextSpotify?.upstreamHlsUrl ?? "",
        publicServerUrl: publicServer.publicServerUrl ?? "",
      });
      setStatus("saved");
      if (spotifyChanged) onChannelChanged?.();
    } catch (err) {
      setStatus(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }

  const numberInput = (key: NumericSettingKey) => (
    <input
      type="number"
      min={1}
      value={draft[key]}
      disabled={busy || !loaded}
      onChange={(event) => setField(key, event.target.value)}
    />
  );

  const np = spotify?.nowPlaying;
  const spotifyControl = (
    <div className={styles["spotify-cell"]}>
      <input
        type="url"
        value={draft.spotifyUrl}
        disabled={busy || !loaded}
        placeholder="https://example.com/stream.m3u8"
        onChange={(event) => {
          setField("spotifyUrl", event.target.value);
          setProbe(null);
        }}
      />
      <div className={styles["spotify-cell-meta"]}>
        <button
          type="button"
          className="link-button"
          disabled={busy || probing || !draft.spotifyUrl.trim()}
          onClick={() => void testSpotify()}
        >
          {probing ? "Testing…" : "Test reachability"}
        </button>
        {spotify?.configured && spotify.status && (
          <span className={`status status-${spotify.status}`}>{spotify.status}</span>
        )}
      </div>
      {probe && (
        <span className={probe.ok ? "success" : "warn"}>
          {probe.ok ? "✓ " : "⚠ "}
          {probe.text}
        </span>
      )}
      {spotify?.configured && np?.title && (
        <span className="muted">
          Now playing: {np.artist ? `${np.title} — ${np.artist}` : np.title}
        </span>
      )}
    </div>
  );

  const rows: { label: string; control: ReactNode; description: string }[] = [
    { label: "Horizon hours", control: numberInput("horizonHours"), description: SETTINGS_HELP.horizonHours },
    { label: "Low-water hours", control: numberInput("lowWaterHours"), description: SETTINGS_HELP.lowWaterHours },
    { label: "Tick seconds", control: numberInput("tickSeconds"), description: SETTINGS_HELP.tickSeconds },
    { label: "Sweep interval (seconds)", control: numberInput("sweepIntervalSeconds"), description: SETTINGS_HELP.sweepIntervalSeconds },
    { label: "Max attempts", control: numberInput("maxAttempts"), description: SETTINGS_HELP.maxAttempts },
    {
      label: "Subtitle languages",
      control: (
        <input
          type="text"
          value={draft.subtitleLanguages}
          disabled={busy || !loaded}
          placeholder="eng, spa"
          onChange={(event) => setField("subtitleLanguages", event.target.value)}
        />
      ),
      description: SETTINGS_HELP.subtitleLanguages,
    },
    {
      label: "Auto-enable subtitles",
      control: (
        <select
          value={draft.subtitleAutoEnable ? "yes" : "no"}
          disabled={busy || !loaded}
          onChange={(event) => setField("subtitleAutoEnable", event.target.value === "yes")}
        >
          <option value="yes">Yes</option>
          <option value="no">No</option>
        </select>
      ),
      description: SETTINGS_HELP.subtitleAutoEnable,
    },
    { label: "Spotify URL", control: spotifyControl, description: SETTINGS_HELP.spotifyUrl },
    {
      label: "Public server URL",
      control: (
        <input
          type="url"
          value={draft.publicServerUrl}
          disabled={busy || !loaded}
          placeholder={typeof window !== "undefined" ? window.location.origin : "http://localhost:8080"}
          onChange={(event) => setField("publicServerUrl", event.target.value)}
        />
      ),
      description: SETTINGS_HELP.publicServerUrl,
    },
  ];

  return (
    <section className="admin-panel-section">
      <form onSubmit={(event) => void save(event)}>
        <div className="section-headline">
          <div className="section-headline-main">
            <h2>Settings</h2>
            <p className="section-purpose">
              Operational tunables for the scheduler, encoder sweeper, subtitles, IPTV URLs, and the Spotify channel.
            </p>
          </div>
          <div className="section-headline-actions">
            {status && <span className="muted">{status}</span>}
            <button type="submit" disabled={busy || !loaded}>
              {busy ? "Saving…" : "Save"}
            </button>
          </div>
        </div>
        <table className={styles["settings-table"]}>
          <thead>
            <tr>
              <th scope="col">Setting</th>
              <th scope="col">Value</th>
              <th scope="col">Description</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((row) => (
              <tr key={row.label}>
                <th scope="row">{row.label}</th>
                <td className={styles["settings-value"]}>{row.control}</td>
                <td className={styles["settings-desc"]}>{row.description}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </form>
    </section>
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
        <div>
          <span>package cache</span>
          <strong>{formatBytes(summary.packageRootBytes ?? summary.packageBytes)}</strong>
        </div>
        <div>
          <span>package folders</span>
          <strong>{summary.packageRootCount}</strong>
        </div>
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
            {cleanupBusy ? "cleaning..." : "Clean up invalid profiles"}
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

function parseSubtitleLanguageText(text: string):
  | { valid: true; languages: string[] }
  | { valid: false; message: string } {
  const languages = text
    .split(/[,\s]+/)
    .map((s) => s.trim().toLowerCase())
    .filter(Boolean);
  const unique = Array.from(new Set(languages));
  if (unique.length === 0) return { valid: false, message: "enter at least one language code" };
  const invalid = unique.find((l) => !/^[a-z]{3}$/.test(l));
  if (invalid) return { valid: false, message: `${invalid} must be a 3-letter ISO 639-2 code` };
  return { valid: true, languages: unique };
}
