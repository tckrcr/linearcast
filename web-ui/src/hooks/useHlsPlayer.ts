import { useEffect } from "react";
import type Hls from "hls.js";
import type { ErrorData, Fragment, LevelLoadedData } from "hls.js";
import type { PlaybackStats } from "../types";

const NETWORK_RETRY_INITIAL_MS = 5_000;
const NETWORK_RETRY_MAX_MS = 60_000;

const fragRetry = {
  maxNumRetry: 3,
  retryDelayMs: 500,
  maxRetryDelayMs: 4_000,
  backoff: "exponential" as const,
};

const playlistRetry = {
  maxNumRetry: 4,
  retryDelayMs: 500,
  maxRetryDelayMs: 4_000,
  backoff: "exponential" as const,
};

const HLS_CONFIG = {
  lowLatencyMode: false,
  enableWorker: true,
  startFragPrefetch: false,
  backBufferLength: 0,
  liveBackBufferLength: 0,
  frontBufferFlushThreshold: 30,
  maxBufferLength: 24,
  maxMaxBufferLength: 30,
  maxBufferSize: 60 * 1000 * 1000,
  liveSyncDurationCount: 6,
  liveMaxLatencyDurationCount: 12,
  maxBufferHole: 0.5,
  nudgeOffset: 0.1,
  nudgeMaxRetry: 5,
  fragLoadPolicy: {
    default: {
      maxTimeToFirstByteMs: 10_000,
      maxLoadTimeMs: 30_000,
      timeoutRetry: { ...fragRetry, retryDelayMs: 0, maxRetryDelayMs: 0 },
      errorRetry: fragRetry,
    },
  },
  manifestLoadPolicy: {
    default: {
      maxTimeToFirstByteMs: 10_000,
      maxLoadTimeMs: 20_000,
      timeoutRetry: { ...playlistRetry, retryDelayMs: 0, maxRetryDelayMs: 0 },
      errorRetry: playlistRetry,
    },
  },
  playlistLoadPolicy: {
    default: {
      maxTimeToFirstByteMs: 10_000,
      maxLoadTimeMs: 20_000,
      timeoutRetry: { ...playlistRetry, retryDelayMs: 0, maxRetryDelayMs: 0 },
      errorRetry: playlistRetry,
    },
  },
};

// Module-level promise cache — fires once on first call, then the browser
// module cache makes every subsequent call free. Kicked off immediately on
// player mount so the download races the stream probe rather than serialising
// after it.
let hlsModulePromise: Promise<typeof import("hls.js")> | null = null;
function warmHls() {
  if (!hlsModulePromise) hlsModulePromise = import("hls.js");
  return hlsModulePromise;
}

type UseHlsPlayerOptions = {
  source: string;
  enabled: boolean;
  autoPlay: boolean;
  muted: boolean;
  videoRef: React.RefObject<HTMLVideoElement | null>;
  hlsRef: React.RefObject<Hls | null>;
  initialStats: PlaybackStats;
  setStats: React.Dispatch<React.SetStateAction<PlaybackStats>>;
};

export function useHlsPlayer({
  source,
  enabled,
  autoPlay,
  muted,
  videoRef,
  hlsRef,
  initialStats,
  setStats,
}: UseHlsPlayerOptions) {
  useEffect(() => {
    const video = videoRef.current;
    if (!video) return;

    video.muted = muted;
    video.autoplay = autoPlay;
    hlsRef.current?.destroy();
    hlsRef.current = null;
    video.removeAttribute("src");
    video.load();
    setStats(initialStats);

    // Start warming the hls.js module immediately — even when !enabled —
    // so the chunk download overlaps with the stream probe polling round-trip.
    void warmHls();

    if (!enabled || !source) {
      return () => {
        hlsRef.current?.destroy();
        hlsRef.current = null;
      };
    }

    let destroyed = false;
    // Mutable container so the async init can hand off its cleanup handle to
    // the synchronous effect teardown.
    const state = { cleanupNetworkRetry: undefined as (() => void) | undefined };

    void warmHls().then(({ default: Hls, Events, ErrorDetails }) => {
      if (destroyed) return;

      // flushAroundPlayhead lives here because it needs Events from the
      // dynamic import.
      function flushAroundPlayhead(hls: InstanceType<typeof Hls>, vid: HTMLVideoElement) {
        const currentTime = vid.currentTime;
        const backBufferEnd = Math.max(0, currentTime - 1);
        hls.stopLoad();
        hls.trigger(Events.BUFFER_FLUSHING, {
          startOffset: 0,
          endOffset: backBufferEnd,
          type: null,
        });
        hls.trigger(Events.BUFFER_FLUSHING, {
          startOffset: currentTime + 12,
          endOffset: Infinity,
          type: null,
        });
      }

      if (Hls.isSupported()) {
        const hls = new Hls(HLS_CONFIG);
        hlsRef.current = hls;
        let networkRetryTimer: number | undefined;
        let networkRetryDelayMs = NETWORK_RETRY_INITIAL_MS;

        function clearNetworkRetry() {
          if (networkRetryTimer != null) {
            window.clearTimeout(networkRetryTimer);
            networkRetryTimer = undefined;
          }
        }
        state.cleanupNetworkRetry = clearNetworkRetry;

        function scheduleNetworkRetry() {
          if (networkRetryTimer != null) return;
          const delayMs = networkRetryDelayMs;
          networkRetryDelayMs = Math.min(NETWORK_RETRY_MAX_MS, networkRetryDelayMs * 2);
          networkRetryTimer = window.setTimeout(() => {
            networkRetryTimer = undefined;
            hls.startLoad();
          }, delayMs);
        }

        setStats((prev) => ({ ...prev, playbackEngine: `hls.js ${Hls.version}` }));

        hls.on(Events.MEDIA_ATTACHED, () => {
          hls.loadSource(source);
        });
        hls.on(Events.MANIFEST_PARSED, () => {
          requestPlayback(video, autoPlay);
        });
        hls.on(Events.FRAG_BUFFERED, (_event, data) => {
          clearNetworkRetry();
          networkRetryDelayMs = NETWORK_RETRY_INITIAL_MS;
          setStats((prev) => ({
            ...prev,
            lastFrag: fragLabel(data.frag),
            lastEvent: "frag buffered",
            streamUnavailable: false,
            streamUnavailableReason: "",
          }));
          requestPlayback(video, autoPlay);
        });
        hls.on(Events.LEVEL_LOADED, (_event, data: LevelLoadedData) => {
          setStats((prev) => ({
            ...prev,
            lastEvent: data.details.live ? "live level loaded" : "vod level loaded",
          }));
        });
        hls.on(Events.ERROR, (_event, data: ErrorData) => {
          const frag = "frag" in data && data.frag ? ` frag=${fragLabel(data.frag)}` : "";
          const reason = data.error ? ` ${data.error.message}` : "";
          const msg = `${data.type}: ${data.details}${frag}${reason}${data.fatal ? " fatal" : ""}`;
          setStats((prev) => ({
            ...prev,
            lastEvent: "error",
            errors: [msg, ...prev.errors].slice(0, 6),
          }));
          if (data.details === ErrorDetails.BUFFER_FULL_ERROR) {
            flushAroundPlayhead(hls, video);
            hls.startLoad(video.currentTime);
            return;
          }
          if (isMissingPackageArtifact(data)) {
            setStats((prev) => ({
              ...prev,
              lastEvent: "stream unavailable",
              streamUnavailable: true,
              streamUnavailableReason: streamUnavailableReason(data),
            }));
            hls.stopLoad();
            return;
          }
          if (data.fatal) {
            if (data.type === "networkError") {
              setStats((prev) => ({
                ...prev,
                lastEvent: "stream unavailable",
                streamUnavailable: true,
                streamUnavailableReason: "",
              }));
              hls.stopLoad();
              scheduleNetworkRetry();
            } else if (data.type === "mediaError") {
              hls.recoverMediaError();
            }
          }
        });

        hls.attachMedia(video);
      } else if (video.canPlayType("application/vnd.apple.mpegurl")) {
        video.src = source;
        video.autoplay = autoPlay;
        setStats((prev) => ({ ...prev, playbackEngine: "native hls" }));
        requestPlayback(video, autoPlay);
      }
    });

    return () => {
      destroyed = true;
      state.cleanupNetworkRetry?.();
      hlsRef.current?.destroy();
      hlsRef.current = null;
    };
  }, [source, enabled, autoPlay, muted, videoRef, hlsRef, initialStats, setStats]);
}

function streamUnavailableReason(data: ErrorData): string {
  if (isMissingPackageArtifact(data)) {
    return "Package segments are missing. Restart the packager worker to re-scan and rebuild this episode.";
  }
  return "";
}

function isMissingPackageArtifact(data: ErrorData): boolean {
  if (data.details !== "fragLoadError") return false;
  const responseCode = "response" in data ? data.response?.code : undefined;
  return responseCode === 404;
}

function fragLabel(fragment?: Fragment | null): string {
  if (!fragment) return "";
  const sn = typeof fragment.sn === "number" ? fragment.sn : String(fragment.sn);
  return `${sn} @ ${fragment.start.toFixed(2)}s`;
}

function requestPlayback(video: HTMLVideoElement, autoPlay: boolean) {
  if (!autoPlay || !video.paused) return;
  void video.play().catch(() => undefined);
}
