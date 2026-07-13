import { formatSeconds } from "./format";
import type { PlaybackStats, StreamProbe } from "./types";

type Props = {
  open: boolean;
  onClose: () => void;
  stats: PlaybackStats;
  probe: StreamProbe;
  appliedSource: string;
};

type DebugRow = {
  name: string;
  value: string;
  title?: string;
};

export function DebugDrawer({
  open,
  onClose,
  stats,
  probe,
  appliedSource,
}: Props) {
  if (!open) return null;

  const rows: DebugRow[] = [
    { name: "Stream Status", value: probe.detail ? `${probe.status} · ${probe.detail}` : probe.status },
    { name: "Stream Unavailable", value: stats.streamUnavailable ? stats.streamUnavailableReason || "yes" : "no", title: stats.streamUnavailableReason },
    { name: "Fatal Error", value: stats.fatalError || "-", title: stats.fatalError },
    { name: "Applied Source", value: appliedSource || "-", title: appliedSource },
    { name: "Playback Engine", value: stats.playbackEngine || "-" },
    { name: "Ready State", value: String(stats.readyState) },
    { name: "Playback State", value: stats.paused ? "Paused" : "Playing" },
    { name: "Current Time", value: formatSeconds(stats.currentTime) },
    { name: "Playback Rate", value: `${stats.playbackRate.toFixed(2)}×` },
    { name: "Video Resolution", value: formatResolution(stats.videoWidth, stats.videoHeight) },
    { name: "Player Resolution", value: formatResolution(stats.playerWidth, stats.playerHeight) },
    { name: "Viewport Resolution", value: formatResolution(stats.viewportWidth, stats.viewportHeight) },
    { name: "Buffer Ahead", value: formatSeconds(stats.bufferAhead) },
    { name: "Buffered Ranges", value: stats.buffered || "-", title: stats.buffered },
    { name: "HLS Latency", value: formatSeconds(stats.hlsLatency) },
    { name: "Live Sync Position", value: formatSeconds(stats.liveSyncPosition) },
    { name: "Bandwidth Estimate", value: formatKbps(stats.bandwidthEstimate) },
    { name: "Current Level", value: stats.currentLevel == null || stats.currentLevel < 0 ? "auto" : String(stats.currentLevel) },
    { name: "Frames", value: `${stats.droppedFrames} dropped / ${stats.totalFrames} total` },
    { name: "Last Fragment", value: stats.lastFrag || "-", title: stats.lastFrag },
    { name: "Last Event", value: stats.lastEvent || "-", title: stats.lastEvent },
  ];

  return (
    <aside className="drawer drawer-debug" role="dialog" aria-label="Debug panel">
      <header className="drawer-head drawer-debug-head">
        <h2>Debug Stats</h2>
        <button type="button" className="drawer-close" onClick={onClose} aria-label="Close debug panel">
          ×
        </button>
      </header>

      <table className="debug-stats-table">
        <thead>
          <tr>
            <th scope="col">Name</th>
            <th scope="col">Value</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((row) => (
            <tr key={row.name}>
              <td>{row.name}</td>
              <td className="ellipsis" title={row.title ?? row.value}>{row.value}</td>
            </tr>
          ))}
        </tbody>
      </table>

      <section className="drawer-section debug-errors-section">
        <h3>Recent Errors</h3>
        {stats.errors.length > 0 ? (
          <ul className="drawer-errors">
            {stats.errors.map((error, index) => (
              <li key={`${error}-${index}`}>{error}</li>
            ))}
          </ul>
        ) : (
          <p className="debug-no-errors">No recent playback errors.</p>
        )}
      </section>
    </aside>
  );
}

function formatResolution(width: number, height: number): string {
  if (!width || !height) return "-";
  return `${width}×${height}`;
}

function formatKbps(bitsPerSecond: number | null): string {
  if (bitsPerSecond == null || !Number.isFinite(bitsPerSecond) || bitsPerSecond <= 0) return "-";
  return `${Math.round(bitsPerSecond / 1000)} Kbps`;
}
