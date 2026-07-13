import { useCallback, useEffect, useRef, useState } from "react";
import type Hls from "hls.js";
import { useHlsPlayer } from "./hooks/useHlsPlayer";
import { usePlaybackStats } from "./hooks/usePlaybackStats";
import { PlayerControls } from "./PlayerControls";
import { formatMs, mediaTitle } from "./format";
import type { LiveSlot } from "./playbackClock";
import type { PlayableSource, PlaybackStats, StreamProbe } from "./types";

const initialStats: PlaybackStats = {
  readyState: 0,
  paused: true,
  currentTime: 0,
  playbackRate: 1,
  videoWidth: 0,
  videoHeight: 0,
  playerWidth: 0,
  playerHeight: 0,
  viewportWidth: 0,
  viewportHeight: 0,
  droppedFrames: 0,
  totalFrames: 0,
  bufferAhead: 0,
  buffered: "",
  hlsLatency: null,
  liveSyncPosition: null,
  bandwidthEstimate: null,
  currentLevel: null,
  lastFrag: "",
  lastEvent: "",
  playbackEngine: "",
  errors: [],
  streamUnavailable: false,
  streamUnavailableReason: "",
  fatalError: "",
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
  nowSlot: LiveSlot;
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
  nowSlot,
  hasSources,
  onStats,
  videoRef,
  hlsRef,
}: PlayerProps) {
  const [stats, setStats] = useState<PlaybackStats>(initialStats);
  const [subtitleSwitching, setSubtitleSwitching] = useState(false);
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

  const handleBurnSubtitleSwitch = useCallback(async <T,>(update: () => Promise<T>): Promise<T> => {
    setSubtitleSwitching(true);
    try {
      await nextAnimationFrame();
      const result = await update();
      await waitForManifestReady(source);
      return result;
    } finally {
      setSubtitleSwitching(false);
    }
  }, [source]);

  useHlsPlayer({
    source,
    enabled: probe.status === "ready" && !subtitleSwitching,
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
      {probe.status === "ready" && !subtitleSwitching && !stats.streamUnavailable && !stats.fatalError && (
        <PlayerControls
          channelID={activeSource?.id ?? ""}
          videoRef={videoRef}
          visible={controlsVisible}
          muted={muted}
          abrMode={abrMode}
          abrAvailable={abrAvailable}
          onMutedChange={onMutedChange}
          onAbrModeChange={onAbrModeChange}
          onBurnSubtitleSwitch={handleBurnSubtitleSwitch}
          hlsRef={hlsRef}
        />
      )}
      {subtitleSwitching && !stats.fatalError && (
        <div className="player-overlay">
          <strong>Switching subtitles…</strong>
          <span>Restarting the on-demand encoder.</span>
          <span className="player-overlay-hint">Playback will resume when the new stream is ready.</span>
        </div>
      )}
      {probe.status !== "ready" && !subtitleSwitching && !stats.streamUnavailable && !stats.fatalError && (
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
              {nowSlot.remainingMs != null ? (
                <span className="player-overlay-hint">
                  next up: {mediaTitle(nowSlot.next)} in{" "}
                  {formatMs(nowSlot.remainingMs)}
                </span>
              ) : (
                <span className="player-overlay-hint">waiting for the next episode</span>
              )}
            </>
          ) : (
            <>
              <strong>{waitingTitle(activeSource, probe)}</strong>
              <span>{waitingDetail(activeSource, probe)}</span>
              <span className="player-overlay-hint">{waitingHint(activeSource)}</span>
            </>
          )}
        </div>
      )}
      {stats.fatalError && (
        <div className="player-overlay">
          <strong>Can&apos;t play on this device</strong>
          <span>{stats.fatalError}</span>
          <span className="player-overlay-hint">
            Try a different device, or switch this channel to a profile with an H.264 rendition.
          </span>
        </div>
      )}
      {stats.streamUnavailable && !stats.fatalError && (
        <div className="player-overlay">
          <strong>Stream unavailable</strong>
          {stats.streamUnavailableReason ? (
            <span>{stats.streamUnavailableReason}</span>
          ) : (
            <span>{mediaTitle(activeSource?.current)} can&apos;t be played right now.</span>
          )}
          {nowSlot.remainingMs != null ? (
            <span className="player-overlay-hint">
              next up: {mediaTitle(nowSlot.next)} in{" "}
              {formatMs(nowSlot.remainingMs)}
            </span>
          ) : (
            <span className="player-overlay-hint">waiting for the next episode</span>
          )}
        </div>
      )}
      {muted && probe.status === "ready" && !subtitleSwitching && (
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

function nextAnimationFrame(): Promise<void> {
  return new Promise((resolve) => window.requestAnimationFrame(() => resolve()));
}

function waitingTitle(_source: PlayableSource | null, probe: StreamProbe): string {
  if (probe.status === "checking") return "Tuning in…";
  return "Channel warming up…";
}

function waitingDetail(_source: PlayableSource | null, probe: StreamProbe): string {
  return probe.detail || "waiting for manifest";
}

function waitingHint(_source: PlayableSource | null): string {
  return "retrying every 3s — first segments take ~30s after import";
}

async function waitForManifestReady(source: string): Promise<void> {
  if (!source) return;
  const deadline = Date.now() + 30_000;
  while (Date.now() < deadline) {
    try {
      const res = await fetch(source, { cache: "no-store" });
      if (res.ok) return;
    } catch {
      // Keep polling through transient network errors while the encoder warms.
    }
    await sleep(1_000);
  }
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => window.setTimeout(resolve, ms));
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
