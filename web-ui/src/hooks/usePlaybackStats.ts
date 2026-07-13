import { useEffect } from "react";
import type Hls from "hls.js";
import type { PlaybackStats } from "../types";

export function usePlaybackStats({
  videoRef,
  hlsRef,
  setStats,
}: {
  videoRef: React.RefObject<HTMLVideoElement | null>;
  hlsRef: React.RefObject<Hls | null>;
  setStats: React.Dispatch<React.SetStateAction<PlaybackStats>>;
}) {
  useEffect(() => {
    const id = window.setInterval(() => {
      const video = videoRef.current;
      if (!video) return;
      const quality = video.getVideoPlaybackQuality?.();
      const buffered = bufferedSummary(video);
      const hls = hlsRef.current;
      setStats((prev) => ({
        ...prev,
        readyState: video.readyState,
        paused: video.paused,
        currentTime: video.currentTime,
        playbackRate: video.playbackRate,
        videoWidth: video.videoWidth,
        videoHeight: video.videoHeight,
        playerWidth: Math.round(video.clientWidth),
        playerHeight: Math.round(video.clientHeight),
        viewportWidth: window.innerWidth,
        viewportHeight: window.innerHeight,
        droppedFrames: quality?.droppedVideoFrames ?? 0,
        totalFrames: quality?.totalVideoFrames ?? 0,
        bufferAhead: buffered.ahead,
        buffered: buffered.summary,
        hlsLatency: hls?.latency ?? null,
        liveSyncPosition: hls?.liveSyncPosition ?? null,
        bandwidthEstimate: hls?.bandwidthEstimate ?? null,
        currentLevel: hls?.currentLevel ?? null,
      }));
    }, 500);
    return () => window.clearInterval(id);
  }, [videoRef, hlsRef, setStats]);
}

function bufferedSummary(video: HTMLVideoElement): { ahead: number; summary: string } {
  const ranges: string[] = [];
  let ahead = 0;
  for (let i = 0; i < video.buffered.length; i++) {
    const start = video.buffered.start(i);
    const end = video.buffered.end(i);
    ranges.push(`${start.toFixed(1)}-${end.toFixed(1)}`);
    if (start <= video.currentTime && video.currentTime <= end) {
      ahead = Math.max(0, end - video.currentTime);
    }
  }
  return { ahead, summary: ranges.join(", ") };
}
