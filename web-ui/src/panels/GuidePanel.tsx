import { Fragment, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { getChannelSchedule, useAdminNow } from "../api";
import { formatDateTime, formatMs } from "../format";
import { usePolling } from "../hooks/usePolling";
import type { ChannelNow, ChannelSchedule } from "../types";

const HOUR_MS = 3600 * 1000;
const ROW_HEIGHT_PX = 56;
const RAIL_HEIGHT_PX = 26;
const LABEL_WIDTH_PX = 180;
const VIEW_KEY = "tc.guideWindowHours";
const REFRESH_MS = 60_000;

const WINDOW_OPTIONS: { label: string; hours: number }[] = [
  { label: "3h", hours: 3 },
  { label: "6h", hours: 6 },
  { label: "12h", hours: 12 },
  { label: "24h", hours: 24 },
];

type Entry = ChannelSchedule["entries"][number];

export function GuidePanel({ onChannelSelect }: { onChannelSelect: (id: string) => void }) {
  const { data: adminNow } = useAdminNow(15_000);

  const channels = useMemo<ChannelNow[]>(
    () => (adminNow?.channels ?? []).filter((c) => !c.hiddenFromGuide),
    [adminNow],
  );
  const channelKey = useMemo(() => channels.map((c) => c.id).join(","), [channels]);

  const [windowHours, setWindowHours] = useState<number>(() => {
    try {
      const stored = window.localStorage.getItem(VIEW_KEY);
      const n = stored ? Number(stored) : NaN;
      return WINDOW_OPTIONS.some((o) => o.hours === n) ? n : 6;
    } catch {
      return 6;
    }
  });
  useEffect(() => {
    try {
      window.localStorage.setItem(VIEW_KEY, String(windowHours));
    } catch {
      /* localStorage may be unavailable */
    }
  }, [windowHours]);

  const [windowStartMs, setWindowStartMs] = useState<number>(() => {
    const now = Date.now();
    return now - (now % HOUR_MS);
  });
  const windowEndMs = windowStartMs + windowHours * HOUR_MS;

  const [schedules, setSchedules] = useState<Record<string, Entry[]>>({});
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const requestRef = useRef(0);

  const loadSchedules = useCallback((silent: boolean, signal?: AbortSignal) => {
    if (channels.length === 0) {
      setSchedules({});
      return Promise.resolve();
    }
    const reqId = ++requestRef.current;
    if (!silent) {
      setLoading(true);
      setError("");
    }
    return Promise.all(
      channels
        .filter((channel) => !channel.isExternal)
        .map((channel) =>
          getChannelSchedule(channel.id, windowStartMs, windowHours, signal)
            .then((data) => ({ id: channel.id, entries: data.entries, err: null as string | null }))
            .catch((err) => ({
              id: channel.id,
              entries: [] as Entry[],
              err: err instanceof Error ? err.message : String(err),
            })),
        ),
    ).then((results) => {
      if (requestRef.current !== reqId) return;
      const next: Record<string, Entry[]> = {};
      const errs: string[] = [];
      for (const r of results) {
        next[r.id] = r.entries;
        if (r.err) errs.push(`${r.id}: ${r.err}`);
      }
      setSchedules(next);
      if (!silent) {
        setError(errs.join("; "));
        setLoading(false);
      }
      if (errs.length === results.length && results.length > 0) {
        throw new Error(errs.join("; "));
      }
    });
  }, [channelKey, windowStartMs, windowHours]);

  useEffect(() => { void loadSchedules(false).catch(() => undefined); }, [loadSchedules]);

  usePolling({
    enabled: channels.length > 0,
    intervalMs: REFRESH_MS,
    maxIntervalMs: 5 * REFRESH_MS,
    immediate: false,
    task: (signal) => loadSchedules(true, signal),
  });

  function shiftWindow(deltaHours: number) {
    setWindowStartMs((ms) => ms + deltaHours * HOUR_MS);
  }
  function jumpToNow() {
    const now = Date.now();
    setWindowStartMs(now - (now % HOUR_MS));
  }

  function msToPct(ms: number) {
    return ((ms - windowStartMs) / (windowHours * HOUR_MS)) * 100;
  }

  const ticks: number[] = [];
  for (let h = 0; h <= windowHours; h++) ticks.push(windowStartMs + h * HOUR_MS);

  const nowMs = Date.now();
  const nowVisible = nowMs >= windowStartMs && nowMs <= windowEndMs;

  return (
    <div className="admin-panel">
      <section className="admin-panel-section">
        <div className="section-headline">
          <h2>Guide</h2>
          <div className="guide-window-toggle" role="tablist" aria-label="Window size">
            {WINDOW_OPTIONS.map((opt) => (
              <button
                key={opt.hours}
                type="button"
                role="tab"
                aria-selected={windowHours === opt.hours}
                className={`guide-window-tab${windowHours === opt.hours ? " is-active" : ""}`}
                onClick={() => setWindowHours(opt.hours)}
              >
                {opt.label}
              </button>
            ))}
          </div>
          <div className="schedule-nav">
            <button type="button" onClick={() => shiftWindow(-windowHours)}>← prev</button>
            <button type="button" onClick={jumpToNow}>now</button>
            <button type="button" onClick={() => shiftWindow(windowHours)}>next →</button>
          </div>
        </div>
        <p className="muted schedule-window-label">
          {formatDateTime(windowStartMs)} – {formatDateTime(windowEndMs)}
        </p>

        {channels.length === 0 ? (
          <p className="muted">no channels to display</p>
        ) : (
          <div className="guide-scroll">
            <div
              className="guide-grid"
              style={{
                gridTemplateColumns: `${LABEL_WIDTH_PX}px 1fr`,
              }}
            >
              {/* Top-left corner (empty above the label column). */}
              <div className="guide-corner" />

              {/* Hour rail. */}
              <div className="guide-rail" style={{ height: `${RAIL_HEIGHT_PX}px` }}>
                {ticks.map((tickMs, idx) => (
                  <Fragment key={tickMs}>
                    <div
                      className="schedule-timeline-tick"
                      style={{ left: `${msToPct(tickMs)}%` }}
                    />
                    {idx < ticks.length - 1 && (
                      <div
                        className="schedule-timeline-tick-label"
                        style={{ left: `${msToPct(tickMs)}%` }}
                      >
                        {formatHourLabel(tickMs)}
                      </div>
                    )}
                  </Fragment>
                ))}
              </div>

              {/* One row per channel: label + track. */}
              {channels.map((channel) => (
                <Fragment key={channel.id}>
                  <button
                    type="button"
                    className="guide-channel-label"
                    style={{ height: `${ROW_HEIGHT_PX}px` }}
                    onClick={() => onChannelSelect(channel.id)}
                    title={`Open ${channel.displayName || channel.id}`}
                  >
                    <span className={`sidebar-dot status-dot-${channel.status}`} />
                    <span className="guide-channel-label-name">
                      {channel.displayName || channel.id}
                      {channel.prefillMode === "on_demand" && (
                        <span className="guide-channel-badge" title="On-demand — packages on tune-in">
                          on-demand
                        </span>
                      )}
                    </span>
                  </button>
                  <div className="guide-track" style={{ height: `${ROW_HEIGHT_PX}px` }}>
                    {renderChannelBlocks(
                      channel,
                      schedules[channel.id] ?? [],
                      windowStartMs,
                      windowEndMs,
                      nowMs,
                      msToPct,
                      onChannelSelect,
                    )}
                  </div>
                </Fragment>
              ))}

              {/* Now-line: absolutely positioned over the track column so it
                  does not consume an auto-placed grid cell. */}
              {nowVisible && (
                <div
                  className="guide-now"
                  style={{
                    left: `calc(${LABEL_WIDTH_PX}px + (100% - ${LABEL_WIDTH_PX}px) * ${msToPct(nowMs) / 100})`,
                  }}
                  aria-hidden
                />
              )}
            </div>
          </div>
        )}

        {loading && Object.keys(schedules).length === 0 && (
          <p className="muted">loading schedules…</p>
        )}
        {error && <p className="error">{error}</p>}
      </section>
    </div>
  );
}

function renderChannelBlocks(
  channel: ChannelNow,
  entries: Entry[],
  windowStartMs: number,
  windowEndMs: number,
  nowMs: number,
  msToPct: (ms: number) => number,
  onChannelSelect: (id: string) => void,
) {
  if (channel.isExternal) {
    const np = channel.nowPlaying;
    const label = np?.title
      ? np.artist ? `${np.title} — ${np.artist}` : np.title
      : "live";
    return (
      <button
        type="button"
        className="schedule-timeline-entry is-live"
        style={{ left: "0%", width: "100%" }}
        onClick={() => onChannelSelect(channel.id)}
        title={label}
      >
        <span className="schedule-timeline-entry-title">{label}</span>
      </button>
    );
  }

  if (entries.length === 0) {
    return (
      <div
        className="schedule-timeline-unscheduled"
        style={{ left: "0%", width: "100%" }}
        title="no schedule"
      />
    );
  }

  const out: React.ReactNode[] = [];
  let cursor = windowStartMs;
  for (let i = 0; i < entries.length; i++) {
    const entry = entries[i];
    const startMs = Math.max(entry.startMs, windowStartMs);
    const endMs = Math.min(entry.endMs, windowEndMs);
    if (endMs <= windowStartMs || startMs >= windowEndMs) continue;
    if (startMs > cursor) {
      const leftPct = msToPct(cursor);
      const widthPct = msToPct(startMs) - leftPct;
      const isNow = cursor <= nowMs && nowMs < startMs;
      out.push(
        <div
          key={`gap-${cursor}-${i}`}
          className={`schedule-timeline-gap${isNow ? " is-now" : ""}`}
          style={{ left: `${leftPct}%`, width: `${widthPct}%` }}
          title={`gap at ${formatDateTime(cursor)} · ${formatMs(startMs - cursor)}`}
        >
          <span className="schedule-timeline-gap-label">gap</span>
        </div>,
      );
    }
    const leftPct = msToPct(startMs);
    const widthPct = msToPct(endMs) - leftPct;
    const isNow = startMs <= nowMs && nowMs < endMs;
    out.push(
      <button
        key={entry.entryId}
        type="button"
        className={`schedule-timeline-entry${isNow ? " is-now" : ""}`}
        style={{ left: `${leftPct}%`, width: `${widthPct}%` }}
        onClick={() => onChannelSelect(channel.id)}
        title={`${formatDateTime(entry.startMs)} · ${entry.title || entry.mediaId} · ${formatMs(entry.durationMs)}`}
      >
        <span className="schedule-timeline-entry-title">
          {entry.title || entry.mediaId}
        </span>
        <span className="schedule-timeline-entry-meta muted">
          {formatMs(entry.durationMs)}
        </span>
      </button>,
    );
    cursor = Math.max(cursor, endMs);
  }
  if (cursor < windowEndMs) {
    const leftPct = msToPct(cursor);
    const widthPct = msToPct(windowEndMs) - leftPct;
    const isNow = cursor <= nowMs && nowMs < windowEndMs;
    out.push(
      <div
        key={`gap-tail-${cursor}`}
        className={`schedule-timeline-gap${isNow ? " is-now" : ""}`}
        style={{ left: `${leftPct}%`, width: `${widthPct}%` }}
        title={`gap at ${formatDateTime(cursor)} · ${formatMs(windowEndMs - cursor)}`}
      >
        <span className="schedule-timeline-gap-label">gap</span>
      </div>,
    );
  }
  return out;
}

function formatHourLabel(ms: number): string {
  const d = new Date(ms);
  const h = d.getHours();
  if (h === 0) {
    return `${d.toLocaleDateString(undefined, { month: "short", day: "numeric" })} · 12a`;
  }
  if (h === 12) return "12p";
  return h < 12 ? `${h}a` : `${h - 12}p`;
}
