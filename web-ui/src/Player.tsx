import { useEffect, useRef, useState } from "react";
import type Hls from "hls.js";
import { useHlsPlayer } from "./hooks/useHlsPlayer";
import { usePlaybackStats } from "./hooks/usePlaybackStats";
import { PlayerControls } from "./PlayerControls";
import { formatMs, mediaTitle } from "./format";
import type { PlayableSource, PlaybackStats, StreamProbe } from "./types";

const initialStats: PlaybackStats = {
  readyState: 0,
  paused: true,
  currentTime: 0,
  playbackRate: 1,
  droppedFrames: 0,
  totalFrames: 0,
  bufferAhead: 0,
  buffered: "",
  hlsLatency: null,
  liveSyncPosition: null,
  lastFrag: "",
  lastEvent: "",
  playbackEngine: "",
  errors: [],
  streamUnavailable: false,
  streamUnavailableReason: "",
};

type PlayerProps = {
  source: string;
  autoPlay: boolean;
  muted: boolean;
  abrMode: "best" | "saver";
  abrAvailable: boolean;
  controlsVisible: boolean;
  onMutedChange: (muted: boolean) => void;
  onAbrModeChange: (mode: "best" | "saver") => void;
  probe: StreamProbe;
  activeSource: PlayableSource | null;
  hasSources: boolean;
  onStats: (stats: PlaybackStats) => void;
  videoRef: React.RefObject<HTMLVideoElement | null>;
  hlsRef: React.RefObject<Hls | null>;
};

export function Player({
  source,
  autoPlay,
  muted,
  abrMode,
  abrAvailable,
  controlsVisible,
  onMutedChange,
  onAbrModeChange,
  probe,
  activeSource,
  hasSources,
  onStats,
  videoRef,
  hlsRef,
}: PlayerProps) {
  const [stats, setStats] = useState<PlaybackStats>(initialStats);
  const onStatsRef = useRef(onStats);
  onStatsRef.current = onStats;

  // Keep video.muted in sync with the prop. React doesn't reliably forward
  // the `muted` attribute on subsequent renders, and the value at the moment
  // hls.js calls play() determines whether autoplay is allowed.
  useEffect(() => {
    if (videoRef.current) videoRef.current.muted = muted;
  }, [muted, videoRef]);

  // Sync state back when the user toggles mute via the native controls.
  useEffect(() => {
    const video = videoRef.current;
    if (!video) return;
    function onVolumeChange() {
      if (video!.muted !== muted) onMutedChange(video!.muted);
    }
    video.addEventListener("volumechange", onVolumeChange);
    return () => video.removeEventListener("volumechange", onVolumeChange);
  }, [muted, onMutedChange, videoRef]);

  useHlsPlayer({
    source,
    enabled: probe.status === "ready",
    autoPlay,
    muted,
    abrMode,
    videoRef,
    hlsRef,
    initialStats,
    setStats,
  });
  usePlaybackStats({ videoRef, hlsRef, setStats });

  useEffect(() => {
    onStatsRef.current(stats);
  }, [stats]);

  return (
    <div className="player">
      <video ref={videoRef} playsInline muted={muted} />
      {probe.status === "ready" && !stats.streamUnavailable && (
        <PlayerControls
          channelID={activeSource?.id ?? ""}
          videoRef={videoRef}
          visible={controlsVisible}
          muted={muted}
          abrMode={abrMode}
          abrAvailable={abrAvailable}
          onMutedChange={onMutedChange}
          onAbrModeChange={onAbrModeChange}
        />
      )}
      {probe.status !== "ready" && !stats.streamUnavailable && (
        <div className="player-overlay">
          {!hasSources ? (
            <>
              <strong>No channels configured</strong>
              <span>Create a channel in the admin panel to get started.</span>
            </>
          ) : probe.detail === "manifest 503" && activeSource?.current ? (
            <>
              <strong>Stream unavailable</strong>
              <span>{unavailableReason(activeSource)}</span>
              {activeSource.current.remainingMs != null ? (
                <span className="player-overlay-hint">
                  next up: {mediaTitle(activeSource.next)} in{" "}
                  {formatMs(activeSource.current.remainingMs)}
                </span>
              ) : (
                <span className="player-overlay-hint">waiting for the next episode</span>
              )}
            </>
          ) : (
            <>
              <strong>{probe.status === "checking" ? "Tuning in…" : "Channel warming up…"}</strong>
              <span>{probe.detail || "waiting for manifest"}</span>
              <span className="player-overlay-hint">
                retrying every 3s — first segments take ~30s after import
              </span>
            </>
          )}
        </div>
      )}
      {stats.streamUnavailable && (
        <div className="player-overlay">
          <strong>Stream unavailable</strong>
          {stats.streamUnavailableReason ? (
            <span>{stats.streamUnavailableReason}</span>
          ) : (
            <span>{mediaTitle(activeSource?.current)} can&apos;t be played right now.</span>
          )}
          {activeSource?.current?.remainingMs != null ? (
            <span className="player-overlay-hint">
              next up: {mediaTitle(activeSource.next)} in{" "}
              {formatMs(activeSource.current.remainingMs)}
            </span>
          ) : (
            <span className="player-overlay-hint">waiting for the next episode</span>
          )}
        </div>
      )}
      {muted && probe.status === "ready" && (
        <button
          type="button"
          className="unmute-pill"
          onClick={() => onMutedChange(false)}
          aria-label="Unmute"
        >
          <span aria-hidden>🔇</span> Click for sound
        </button>
      )}
    </div>
  );
}

function unavailableReason(source: PlayableSource): string {
  const current = source.current;
  if (!current) return "This channel can't be played right now.";
  if (isSourceUnavailable(current.packageError)) {
    return `${mediaTitle(current)} can't be played because the source file is unavailable.`;
  }
  if (current.packageStatus === "pending" || current.packageStatus === "processing") {
    return `${mediaTitle(current)} is being rebuilt.`;
  }
  return `${mediaTitle(current)} can't be played right now.`;
}

function isSourceUnavailable(error?: string): boolean {
  return (error || "").toLowerCase().includes("source file unavailable");
}
