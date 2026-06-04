import type Hls from "hls.js";
import { formatSeconds } from "./format";
import type { PlaybackStats, StreamProbe } from "./types";

type Props = {
  open: boolean;
  onClose: () => void;
  stats: PlaybackStats;
  probe: StreamProbe;
  appliedSource: string;
  autoPlay: boolean;
  onAutoPlayChange: (value: boolean) => void;
  videoRef: React.RefObject<HTMLVideoElement | null>;
  hlsRef: React.RefObject<Hls | null>;
};

export function DebugDrawer({
  open,
  onClose,
  stats,
  probe,
  appliedSource,
  autoPlay,
  onAutoPlayChange,
  videoRef,
  hlsRef,
}: Props) {
  if (!open) return null;

  function jumpBehindLive() {
    const hls = hlsRef.current;
    const video = videoRef.current;
    if (!hls || !video || hls.liveSyncPosition == null) return;
    video.currentTime = Math.max(0, hls.liveSyncPosition - 6);
  }

  return (
    <aside className="drawer drawer-debug" role="dialog" aria-label="Debug panel">
      <header className="drawer-head">
        <h2>Debug</h2>
        <button type="button" className="drawer-close" onClick={onClose} aria-label="Close debug panel">
          ×
        </button>
      </header>

      <section className="drawer-section">
        <h3>Controls</h3>
        <div className="drawer-controls">
          <button type="button" onClick={() => void videoRef.current?.play()}>
            Play
          </button>
          <button type="button" onClick={() => videoRef.current?.pause()}>
            Pause
          </button>
          <button type="button" onClick={jumpBehindLive}>
            −6s live
          </button>
          <label>
            <input
              type="checkbox"
              checked={autoPlay}
              onChange={(event) => onAutoPlayChange(event.target.checked)}
            />
            autoplay
          </label>
        </div>
      </section>

      <section className="drawer-section">
        <h3>Stream probe</h3>
        <div className="kv">
          <span>status</span>
          <strong>{probe.status}</strong>
          <span>detail</span>
          <strong>{probe.detail || "-"}</strong>
          <span>applied</span>
          <strong className="ellipsis" title={appliedSource}>
            {appliedSource}
          </strong>
        </div>
      </section>

      <section className="drawer-section">
        <h3>Playback</h3>
        <div className="kv">
          <span>engine</span>
          <strong>{stats.playbackEngine || "-"}</strong>
          <span>readyState</span>
          <strong>{stats.readyState}</strong>
          <span>paused</span>
          <strong>{String(stats.paused)}</strong>
          <span>currentTime</span>
          <strong>{formatSeconds(stats.currentTime)}</strong>
          <span>bufferAhead</span>
          <strong>{formatSeconds(stats.bufferAhead)}</strong>
          <span>buffered</span>
          <strong className="ellipsis" title={stats.buffered}>
            {stats.buffered || "-"}
          </strong>
          <span>frames</span>
          <strong>
            {stats.droppedFrames} / {stats.totalFrames}
          </strong>
          <span>hls latency</span>
          <strong>{formatSeconds(stats.hlsLatency)}</strong>
          <span>live sync</span>
          <strong>{formatSeconds(stats.liveSyncPosition)}</strong>
          <span>last frag</span>
          <strong>{stats.lastFrag || "-"}</strong>
          <span>last event</span>
          <strong>{stats.lastEvent || "-"}</strong>
        </div>
      </section>

      {stats.errors.length > 0 && (
        <section className="drawer-section">
          <h3>Recent errors</h3>
          <ul className="drawer-errors">
            {stats.errors.map((error, index) => (
              <li key={`${error}-${index}`}>{error}</li>
            ))}
          </ul>
        </section>
      )}
    </aside>
  );
}
