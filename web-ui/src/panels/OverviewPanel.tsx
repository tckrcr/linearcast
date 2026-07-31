// The admin landing view: the state of the appliance without a single click.
//
// Everything here already existed behind a panel selection — schedule coverage
// only rendered inside a channel's own panel, encode backlog only in Encoding,
// degraded signals only in Tools — so answering "is anything wrong?" meant
// visiting each channel in turn. This panel answers it on arrival, and the
// attention list carries the fix for the cases where the fix is unambiguous.
//
// It deliberately reads only cheap endpoints. /api/cache/summary would supply
// disk bytes and per-channel encode needs, but it walks the cache and package
// roots on every call, which is not something a landing view should do on a
// timer. Disk usage stays in Tools, behind an explicit refresh.
import { useEffect, useState, type ReactNode } from "react";
import {
  getDegraded,
  getEncoders,
  getPackageStatusCounts,
  getSchedulerTunables,
} from "../api";
import { formatMs, mediaTitle } from "../format";
import { usePolling } from "../hooks/usePolling";
import { StatusBadge, type StatusTone } from "../ui/StatusBadge";
import type {
  ChannelNow,
  DegradedSignal,
  EncoderListResponse,
  RowBusy,
  RowStatus,
} from "../types";
import { SIGNAL_ACTIONS, signalLabel } from "./degradedSignals";
import styles from "./OverviewPanel.module.css";

const POLL_MS = 20_000;
const EXTEND_HOURS = 24;
// Used until the real scheduler low-water setting arrives, and if it fails to.
const FALLBACK_LOW_WATER_HOURS = 6;

// Coverage below the low-water mark is NOT an operator problem: low-water is the
// scheduler's own trigger, and coverage decays a second per second, so every
// healthy channel sails just under the line and gets refilled on the next tick.
// Flagging that made the normal sawtooth look like a fault.
//
// What does need a human is the extender having stopped keeping up — and the
// honest evidence for that is runway near zero, not runway under the threshold
// the extender itself watches. An hour is far longer than any plausible tick
// interval, so a channel still this low has not been refilled for many tries;
// left alone it becomes a viewer-visible gap within the hour.
const URGENT_RUNWAY_HOURS = 1;

function urgentRunwayHours(lowWaterHours: number): number {
  // Never above low-water: an appliance configured with a very short low-water
  // would otherwise make every channel permanently urgent.
  return Math.min(URGENT_RUNWAY_HOURS, lowWaterHours / 2);
}

// A tone means two different things in the two places it is used here — ink on a
// tile's value, an edge on an attention row — so each surface maps it itself
// rather than sharing one class name that would have to do both.
const TILE_TONES: Record<StatusTone, string> = {
  good: styles.toneGood,
  warn: styles.toneWarn,
  danger: styles.toneDanger,
  neutral: styles.toneNeutral,
};

type Props = {
  // Enabled channels as reported by /api/now, already polled by the workspace.
  channels: ChannelNow[];
  loaded: boolean;
  disabledCount: number;
  busy: RowBusy;
  status: RowStatus;
  onSelectChannel: (id: string) => void;
  onOpenPanel: (panel: string) => void;
  onExtend: (channelID: string, hours: number) => void;
};

type Attention = {
  key: string;
  tone: Exclude<StatusTone, "good" | "neutral">;
  title: string;
  detail: string;
  // What the last action on this channel reported, shown in place of the detail
  // so the operator gets the result where they clicked. /api/now needs a tick to
  // catch up, and until it does the row itself still shows the stale numbers.
  note?: string;
  action?: { label: string; run: () => void; busy: boolean };
};

export function OverviewPanel({
  channels,
  loaded,
  disabledCount,
  busy,
  status,
  onSelectChannel,
  onOpenPanel,
  onExtend,
}: Props) {
  const [signals, setSignals] = useState<DegradedSignal[]>([]);
  const [encoders, setEncoders] = useState<EncoderListResponse | null>(null);
  const [statusCounts, setStatusCounts] = useState<Record<string, number>>({});
  const [lowWaterHours, setLowWaterHours] = useState(FALLBACK_LOW_WATER_HOURS);
  const [error, setError] = useState("");

  useEffect(() => {
    let cancelled = false;
    getSchedulerTunables()
      .then((tunables) => {
        if (!cancelled) setLowWaterHours(tunables.lowWaterHours);
      })
      .catch(() => {
        /* keep the fallback threshold */
      });
    return () => {
      cancelled = true;
    };
  }, []);

  usePolling({
    intervalMs: POLL_MS,
    maxIntervalMs: 120_000,
    task: async (signal) => {
      try {
        const [degraded, encoderList, counts] = await Promise.all([
          getDegraded(),
          getEncoders(signal),
          getPackageStatusCounts(signal),
        ]);
        if (signal.aborted) return;
        setSignals(degraded.signals);
        setEncoders(encoderList);
        setStatusCounts(counts);
        setError("");
      } catch (err) {
        if (signal.aborted) return;
        setError(err instanceof Error ? err.message : String(err));
        throw err;
      }
    },
  });

  const playing = channels.filter((c) => c.status === "playing").length;
  const offAir = channels.length - playing;

  // The channel that runs dry first is the number that decides whether the
  // operator has to do anything today, so it leads rather than an average.
  const driest = channels.reduce<ChannelNow | null>(
    (worst, c) => (worst === null || c.scheduleCoverageMs < worst.scheduleCoverageMs ? c : worst),
    null,
  );

  const activePackaging =
    (encoders?.encoders ?? []).reduce((n, e) => n + (e.jobs?.length ?? 0), 0) +
    (encoders?.localWorker.jobs?.length ?? 0);
  const activeOnDemand = (encoders?.onDemandEncodings ?? []).filter((e) => e.processRunning).length;
  const activeEncodes = activePackaging + activeOnDemand;
  const encodersOnline =
    (encoders?.encoders ?? []).filter((e) => e.status === "online").length +
    (encoders?.localWorker.enabled && encoders.localWorker.status === "online" ? 1 : 0);
  const queued = statusCounts.pending ?? 0;
  const failed = statusCounts.failed ?? 0;

  const degradedSignals = signals.filter((s) => s.degraded);

  const urgentHours = urgentRunwayHours(lowWaterHours);

  const attention: Attention[] = [];
  for (const channel of channels) {
    const name = channel.displayName || channel.id;
    const note = status[channel.id] || undefined;
    const extend: Attention["action"] = {
      label: `Extend ${EXTEND_HOURS}h`,
      run: () => onExtend(channel.id, EXTEND_HOURS),
      busy: busy[channel.id] ?? false,
    };
    if (channel.status === "unscheduled") {
      attention.push({
        key: `${channel.id}:unscheduled`,
        tone: "danger",
        title: `${name} has nothing scheduled`,
        detail: "Viewers tuning in now get no program.",
        note,
        action: extend,
      });
    } else if (channel.status === "gap") {
      attention.push({
        key: `${channel.id}:gap`,
        tone: "danger",
        title: `${name} is in a schedule gap`,
        detail: "No program covers the current time.",
        note,
        action: extend,
      });
    } else if (channel.scheduleCoverageHours < urgentHours) {
      attention.push({
        key: `${channel.id}:runway`,
        tone: "warn",
        title: `${name} runs out in ${formatMs(channel.scheduleCoverageMs)}`,
        detail: `The scheduler extends below ${lowWaterHours}h and has not, so it is not keeping this channel filled.`,
        note,
        action: extend,
      });
    }
    // Only pre-encoded channels need ready packages before they can be
    // scheduled. An on-demand channel encodes as it plays, so zero ready
    // packages is its normal resting state, not a problem.
    if (channel.prefillMode === "eager" && channel.packageReadyCount === 0) {
      attention.push({
        key: `${channel.id}:packages`,
        tone: "warn",
        title: `${name} has no encoded programs`,
        detail: `Nothing is ready for the ${channel.packageProfile} profile, so extending finds no media.`,
        action: { label: "Open Encoding", run: () => onOpenPanel("encoding"), busy: false },
      });
    }
  }
  if (failed > 0) {
    attention.push({
      key: "packages:failed",
      tone: "danger",
      title: `${failed} failed ${failed === 1 ? "encode" : "encodes"}`,
      detail: "Failed packages stay out of every schedule until they are retried.",
      action: { label: "Open Encoding", run: () => onOpenPanel("encoding"), busy: false },
    });
  }
  if (queued > 0 && encodersOnline === 0) {
    attention.push({
      key: "encoders:none",
      tone: "warn",
      title: "No encoder online",
      detail: `${queued} ${queued === 1 ? "package is" : "packages are"} queued with no worker to run them.`,
      action: { label: "Open Encoding", run: () => onOpenPanel("encoding"), busy: false },
    });
  }
  for (const signal of degradedSignals) {
    attention.push({
      key: `signal:${signal.signal}`,
      tone: "danger",
      title: signalLabel(signal.signal),
      detail: SIGNAL_ACTIONS[signal.signal]
        ? `${signal.detail} — ${SIGNAL_ACTIONS[signal.signal]}`
        : signal.detail,
      action: { label: "Open Tools", run: () => onOpenPanel("tools"), busy: false },
    });
  }
  attention.sort((a, b) => (a.tone === b.tone ? 0 : a.tone === "danger" ? -1 : 1));

  return (
    <div className="admin-panel">
      <section className="admin-panel-section">
        <div className="section-headline">
          <div className="section-headline-main">
            <h2>Overview</h2>
            <p className="section-purpose">
              What the appliance is doing right now, and anything that needs you.
            </p>
          </div>
        </div>

        <ul className={styles.tiles} aria-label="Summary">
          <Tile
            label="On air"
            value={loaded ? `${playing}/${channels.length}` : "—"}
            tone={offAir > 0 ? "danger" : "good"}
            note={
              offAir > 0
                ? `${offAir} not playing`
                : disabledCount > 0
                  ? `all playing · ${disabledCount} disabled`
                  : "all playing"
            }
          />
          <Tile
            label="Shortest runway"
            value={driest ? formatMs(driest.scheduleCoverageMs) : "—"}
            // Green means full runway, plain ink means mid-refill — the sawtooth
            // under low-water is normal and gets no colour — and red means the
            // extender has stopped keeping up.
            tone={
              !driest
                ? "neutral"
                : driest.scheduleCoverageHours < urgentHours
                  ? "danger"
                  : driest.scheduleCoverageHours < lowWaterHours
                    ? "neutral"
                    : "good"
            }
            note={
              driest
                ? `${driest.displayName || driest.id} · low-water ${lowWaterHours}h`
                : "no enabled channels"
            }
          />
          {/* The value is how many encodes are running, which is neutral news
              either way — so the tone goes on the count that is actually wrong
              rather than reddening a headline "0" because something else failed. */}
          <Tile
            label="Encoding"
            value={`${activeEncodes}`}
            note={
              <>
                <span className={queued > 0 && encodersOnline === 0 ? styles.toneWarn : undefined}>
                  {queued} queued
                </span>
                {" · "}
                <span className={failed > 0 ? styles.toneDanger : undefined}>{failed} failed</span>
                {" · "}
                {encodersOnline} online
              </>
            }
          />
          <Tile
            label="Health"
            value={degradedSignals.length === 0 ? "OK" : `${degradedSignals.length}`}
            tone={degradedSignals.length === 0 ? "good" : "danger"}
            note={
              degradedSignals.length === 0
                ? "no degraded signals"
                : degradedSignals.map((s) => signalLabel(s.signal)).join(" · ")
            }
          />
        </ul>
        {error && <p className={`muted ${styles.error}`}>status unavailable: {error}</p>}
      </section>

      <section className="admin-panel-section">
        <h3>Needs attention</h3>
        {!loaded ? (
          <p className="muted">loading…</p>
        ) : attention.length === 0 ? (
          <p className="muted">
            Nothing needs you. Every enabled channel is playing, and the scheduler is keeping their
            schedules filled.
          </p>
        ) : (
          <ul className={styles.attention} aria-label="Needs attention">
            {attention.map((item) => (
              <li
                key={item.key}
                className={item.tone === "danger" ? styles.rowDanger : styles.rowWarn}
              >
                <div className={styles.attentionText}>
                  <strong>{item.title}</strong>
                  <span className="muted">{item.note ?? item.detail}</span>
                </div>
                {item.action && (
                  <button
                    type="button"
                    disabled={item.action.busy}
                    onClick={item.action.run}
                  >
                    {item.action.busy ? "…" : item.action.label}
                  </button>
                )}
              </li>
            ))}
          </ul>
        )}
      </section>

      <section className="admin-panel-section">
        <div className="section-headline">
          <div className="section-headline-main">
            <h3>Channels</h3>
            <p className="section-purpose">
              Click a channel to open it. Disabled channels stay in the sidebar.
            </p>
          </div>
        </div>
        {channels.length === 0 ? (
          <div className={styles.emptyChannels}>
            <p className="muted">
              {loaded ? "No enabled channels yet." : "loading…"}
            </p>
            {loaded && (
              <button type="button" className="primary" onClick={() => onOpenPanel("schedule")}>
                Create a channel
              </button>
            )}
          </div>
        ) : (
          <div className={styles.tableWrap}>
            <table className={styles.table}>
              <thead>
                <tr>
                  <th>Channel</th>
                  <th>Now playing</th>
                  <th>Up next</th>
                  <th className={styles.numeric}>Runway</th>
                  <th
                    className={styles.numeric}
                    title="Programs already encoded for this channel's profile. On-demand channels encode as they play, so a low number is normal."
                  >
                    Ready
                  </th>
                </tr>
              </thead>
              <tbody>
                {channels.map((channel) => {
                  const dry = channel.scheduleCoverageHours < urgentHours;
                  return (
                    <tr key={channel.id}>
                      <td>
                        <button
                          type="button"
                          className={`link-button ${styles.channelName}`}
                          onClick={() => onSelectChannel(channel.id)}
                        >
                          <span className={`sidebar-dot status-dot-${channel.status}`} />
                          <span>{channel.displayName || channel.id}</span>
                        </button>
                        {channel.status !== "playing" && (
                          <StatusBadge
                            tone={
                              channel.status === "gap" || channel.status === "unscheduled"
                                ? "danger"
                                : "warn"
                            }
                          >
                            {channel.status}
                          </StatusBadge>
                        )}
                        {channel.hiddenFromGuide && <StatusBadge>hidden</StatusBadge>}
                      </td>
                      <td className={styles.program}>{mediaTitle(channel.current)}</td>
                      <td className={styles.program}>{mediaTitle(channel.next)}</td>
                      <td className={`${styles.numeric} ${dry ? "danger" : ""}`}>
                        {formatMs(channel.scheduleCoverageMs)}
                      </td>
                      <td className={styles.numeric}>
                        {channel.prefillMode === "eager" && channel.packageReadyCount === 0 ? (
                          <span className="danger">0</span>
                        ) : (
                          channel.packageReadyCount
                        )}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </section>
    </div>
  );
}

function Tile({
  label,
  value,
  note,
  tone = "neutral",
}: {
  label: string;
  value: string;
  note: ReactNode;
  tone?: StatusTone;
}) {
  return (
    <li className={styles.tile}>
      <span className={styles.tileLabel}>{label}</span>
      <strong className={`${styles.tileValue} ${TILE_TONES[tone]}`}>{value}</strong>
      <span className={styles.tileNote}>{note}</span>
    </li>
  );
}
