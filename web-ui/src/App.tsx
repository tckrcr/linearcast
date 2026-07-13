import { useEffect, useMemo, useRef, useState } from "react";
import type Hls from "hls.js";
import { ChannelBanner } from "./ChannelBanner";
import { ChannelGuide } from "./ChannelGuide";
import { DebugDrawer } from "./DebugDrawer";
import { Player } from "./Player";
import { usePlayableSources, useStreamProbe } from "./api";
import { usePlaybackClock } from "./hooks/usePlaybackClock";
import { usePlayerKeyboardShortcuts } from "./hooks/usePlayerKeyboardShortcuts";
import { resolveLiveSlot } from "./playbackClock";
import type { PlaybackStats } from "./types";

const ACTIVE_CHANNEL_KEY = "tc.activeChannelId";
const MUTED_KEY = "tc.muted";
const ABR_MODE_KEY = "tc.abrMode";
const HINT_SEEN_KEY = "tc.hintSeen";
const IDLE_MS = 3000;
const HINT_MS = 5500;

const emptyStats: PlaybackStats = {
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
};

export function App() {
  const videoRef = useRef<HTMLVideoElement | null>(null);
  const hlsRef = useRef<Hls | null>(null);

  const { data: playableSources, error: sourceError, updatedAt: sourcesUpdatedAt, refresh: refreshSources } =
    usePlayableSources();
  const visibleSourceError = sourceError === "admin api 502" ? "" : sourceError;

  const activeSources = useMemo(
    () => playableSources?.sources ?? [],
    [playableSources],
  );

  const sortedSources = useMemo(
    () =>
      [...activeSources].sort((a, b) =>
        (a.displayName || a.id).localeCompare(b.displayName || b.id),
      ),
    [activeSources],
  );

  const [activeChannelID, setActiveChannelIDState] = useState<string | null>(() => {
    try {
      return window.localStorage.getItem(ACTIVE_CHANNEL_KEY);
    } catch {
      return null;
    }
  });

  function setActiveChannelID(id: string | null) {
    setActiveChannelIDState(id);
    try {
      if (id) window.localStorage.setItem(ACTIVE_CHANNEL_KEY, id);
      else window.localStorage.removeItem(ACTIVE_CHANNEL_KEY);
    } catch {
      /* localStorage may be unavailable */
    }
  }

  const urlSourceOverride = useMemo(() => {
    try {
      return new URLSearchParams(window.location.search).get("src") || "";
    } catch {
      return "";
    }
  }, []);

  useEffect(() => {
    if (sortedSources.length === 0) return;
    const stillActive = activeChannelID
      ? sortedSources.some((c) => c.id === activeChannelID)
      : false;
    if (!stillActive) {
      setActiveChannelID(sortedSources[0].id);
    }
  }, [sortedSources, activeChannelID]);

  const [autoPlay, setAutoPlay] = useState(true);
  const [muted, setMutedState] = useState<boolean>(() => {
    try {
      return window.localStorage.getItem(MUTED_KEY) !== "false";
    } catch {
      return true;
    }
  });

  function setMuted(value: boolean) {
    setMutedState(value);
    try {
      window.localStorage.setItem(MUTED_KEY, String(value));
    } catch {
      /* localStorage may be unavailable */
    }
  }

  const [abrMode, setAbrModeState] = useState<"best" | "saver">(() => {
    try {
      return window.localStorage.getItem(ABR_MODE_KEY) === "saver" ? "saver" : "best";
    } catch {
      return "best";
    }
  });

  function setAbrMode(mode: "best" | "saver") {
    setAbrModeState(mode);
    try {
      window.localStorage.setItem(ABR_MODE_KEY, mode);
    } catch {
      /* localStorage may be unavailable */
    }
  }

  const [debugOpen, setDebugOpen] = useState(false);
  const [channelsOpen, setChannelsOpen] = useState(false);
  const [stats, setStats] = useState<PlaybackStats>(emptyStats);

  const [idle, setIdle] = useState(false);
  useEffect(() => {
    let timer: number | undefined;
    const reset = () => {
      setIdle(false);
      window.clearTimeout(timer);
      timer = window.setTimeout(() => setIdle(true), IDLE_MS);
    };
    const idleNow = () => {
      window.clearTimeout(timer);
      setIdle(true);
    };
    reset();
    window.addEventListener("mousemove", reset);
    window.addEventListener("mousedown", reset);
    window.addEventListener("touchstart", reset);
    window.addEventListener("keydown", reset);
    window.addEventListener("blur", idleNow);
    return () => {
      window.clearTimeout(timer);
      window.removeEventListener("mousemove", reset);
      window.removeEventListener("mousedown", reset);
      window.removeEventListener("touchstart", reset);
      window.removeEventListener("keydown", reset);
      window.removeEventListener("blur", idleNow);
    };
  }, []);

  const [hintVisible, setHintVisible] = useState(false);
  useEffect(() => {
    try {
      if (window.localStorage.getItem(HINT_SEEN_KEY) === "1") return;
    } catch {
      /* localStorage may be unavailable */
    }
    setHintVisible(true);
    const timer = window.setTimeout(() => {
      setHintVisible(false);
      try {
        window.localStorage.setItem(HINT_SEEN_KEY, "1");
      } catch {
        /* ignore */
      }
    }, HINT_MS);
    return () => window.clearTimeout(timer);
  }, []);

  usePlayerKeyboardShortcuts({
    sortedChannels: sortedSources,
    activeChannelID,
    setActiveChannelID,
    muted,
    setMuted,
    abrMode,
    setAbrMode,
    debugOpen,
    setDebugOpen,
    channelsOpen,
    setChannelsOpen,
  });

  const activeSource =
    activeSources.find((c) => c.id === activeChannelID) ?? null;

  // The wall clock currently on screen, ticking every second. Sourced from the
  // playhead's program-date-time when available, falling back to a skew-corrected
  // wall clock anchored to the last poll.
  const clockAnchor = useMemo(
    () =>
      playableSources && sourcesUpdatedAt != null
        ? { serverNowMs: playableSources.nowMs, localAtMs: sourcesUpdatedAt }
        : null,
    [playableSources, sourcesUpdatedAt],
  );
  const nowOnScreenMs = usePlaybackClock(hlsRef, clockAnchor);
  const liveSlot = useMemo(
    () => resolveLiveSlot(activeSource?.current, activeSource?.next, nowOnScreenMs),
    [activeSource?.current, activeSource?.next, nowOnScreenMs],
  );
  // When the on-screen playhead crosses a program boundary, re-poll once so the
  // now/next labels catch up promptly instead of lagging until the next tick.
  const refreshedBoundaryRef = useRef<number | null>(null);
  const currentEndMs = activeSource?.current?.endMs;
  useEffect(() => {
    if (
      liveSlot.rolledPast &&
      currentEndMs != null &&
      refreshedBoundaryRef.current !== currentEndMs
    ) {
      refreshedBoundaryRef.current = currentEndMs;
      refreshSources();
    }
  }, [liveSlot.rolledPast, currentEndMs, refreshSources]);

  const baseSource = urlSourceOverride || activeSource?.manifestUrl || "";
  const appliedSource = baseSource;

  const probe = useStreamProbe(appliedSource || "/__no_source__");

  const overlayOpen = channelsOpen || debugOpen;
  const cornerVisible = !idle || overlayOpen;

  return (
    <div className={`tv-stage${idle && !overlayOpen ? " is-idle" : ""}`}>
      <Player
        source={appliedSource}
        autoPlay={autoPlay}
        muted={muted}
        abrMode={abrMode}
        abrAvailable={activeSource?.adaptiveBitrate ?? false}
        controlsVisible={cornerVisible}
        onMutedChange={setMuted}
        onAbrModeChange={setAbrMode}
        probe={probe}
        activeSource={activeSource}
        nowSlot={liveSlot}
        hasSources={activeSources.length > 0}
        onStats={setStats}
        videoRef={videoRef}
        hlsRef={hlsRef}
      />

      <ChannelBanner
        channel={activeSource}
        slot={liveSlot}
        visible={cornerVisible && !channelsOpen}
      />

      <div className={`tv-corner${cornerVisible ? " is-visible" : ""}`}>
        {visibleSourceError && <span className="tv-corner-error">api: {visibleSourceError}</span>}
        <a className="tv-corner-btn" href="/admin">
          Admin
        </a>
        <button
          type="button"
          className={`tv-corner-btn${channelsOpen ? " is-on" : ""}`}
          onClick={() => setChannelsOpen((v) => !v)}
        >
          {channelsOpen ? "Close" : "Channels"}
        </button>
        <button
          type="button"
          className="tv-corner-btn"
          onClick={() => setMuted(!muted)}
        >
          {muted ? "Unmute" : "Mute"}
        </button>
        <button
          type="button"
          className={`tv-corner-btn${debugOpen ? " is-on" : ""}`}
          onClick={() => setDebugOpen((v) => !v)}
        >
          Debug
        </button>
      </div>

      {channelsOpen && (
        <div className="tv-channels-overlay">
          <div className="tv-channels-overlay-head">
            <h2>Guide</h2>
            <span className="muted">
              {sortedSources.length} channels · click a program to tune in
            </span>
          </div>
          <ChannelGuide
            activeChannelID={activeChannelID}
            onSelect={(id) => {
              setActiveChannelID(id);
              setChannelsOpen(false);
            }}
          />
        </div>
      )}

      <div className={`tv-hint${hintVisible ? " is-visible" : ""}`} aria-hidden={!hintVisible}>
        <kbd>↑</kbd><kbd>↓</kbd> change channel
        <span className="tv-hint-sep">·</span>
        <kbd>M</kbd> mute
        <span className="tv-hint-sep">·</span>
        <kbd>Q</kbd> quality
        <span className="tv-hint-sep">·</span>
        <kbd>F</kbd> fullscreen
      </div>

      <DebugDrawer
        open={debugOpen}
        onClose={() => setDebugOpen(false)}
        stats={stats}
        probe={probe}
        appliedSource={appliedSource}
      />
    </div>
  );
}
