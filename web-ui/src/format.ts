import type { MediaWindow, PlayableSource } from "./types";

export function formatMs(value: number | null | undefined): string {
  if (value == null || Number.isNaN(value)) return "-";
  const sign = value < 0 ? "-" : "";
  let remaining = Math.abs(Math.round(value / 1000));
  const hours = Math.floor(remaining / 3600);
  remaining -= hours * 3600;
  const minutes = Math.floor(remaining / 60);
  const seconds = remaining - minutes * 60;
  if (hours > 0) return `${sign}${hours}h ${minutes}m`;
  if (minutes > 0) return `${sign}${minutes}m ${seconds}s`;
  return `${sign}${seconds}s`;
}

export function formatClock(value: number | null | undefined): string {
  if (value == null || Number.isNaN(value)) return "-";
  return new Date(value).toLocaleTimeString([], {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });
}

export function formatSeconds(value: number | null | undefined): string {
  if (value == null || Number.isNaN(value)) return "-";
  return `${value.toFixed(2)}s`;
}

export function formatBytes(value: number | null | undefined): string {
  if (value == null || Number.isNaN(value)) return "-";
  const sign = value < 0 ? "-" : "";
  let remaining = Math.abs(value);
  const units = ["B", "KB", "MB", "GB", "TB"];
  let unit = 0;
  while (remaining >= 1024 && unit < units.length - 1) {
    remaining /= 1024;
    unit += 1;
  }
  const decimals = unit === 0 || remaining >= 10 ? 0 : 1;
  return `${sign}${remaining.toFixed(decimals)} ${units[unit]}`;
}

export function mediaTitle(media: MediaWindow | null | undefined): string {
  if (!media) return "-";
  return media.title || media.mediaID;
}

export function sourceNowTitle(source: PlayableSource | null | undefined): string {
  if (!source) return "-";
  if (source.nowPlaying?.title) return source.nowPlaying.title;
  return mediaTitle(source.current);
}

export function sourceNowSubtitle(source: PlayableSource | null | undefined): string {
  if (!source?.nowPlaying) return "";
  const parts = [source.nowPlaying.artist, source.nowPlaying.album].filter(Boolean);
  return parts.join(" · ");
}

export function formatDateTime(ms: number): string {
  return new Date(ms).toLocaleString([], {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}

export function streamUrlFor(channelID: string): string {
  return `/hls/channel/${encodeURIComponent(channelID)}/stream.m3u8`;
}
