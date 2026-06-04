import { apiFetch } from "./client";

export type ScannedTrack = {
  index: number;
  codec: string;
  language: string;
  title: string;
  isBitmap: boolean;
};

export type ScannedEpisode = {
  mediaId: string;
  filename: string;
  packaged: boolean;
  tracks: ScannedTrack[];
  extractedLangs?: string[];
};

export type ScannedSeason = {
  name: string;
  episodes: ScannedEpisode[];
};

export type ScannedShow = {
  name: string;
  seasons: ScannedSeason[];
};

export type SubtitleScanStatus = {
  status: "idle" | "running" | "done" | "error";
  scanned: number;
  total: number;
  shows?: ScannedShow[];
  error?: string;
};

export type SubtitleExtractStatus = {
  status: "idle" | "running" | "done" | "error";
  processed: number;
  total: number;
  extracted: number;
  skipped: number;
  failed: number;
  error?: string;
};

export async function getSubtitleScan(signal?: AbortSignal): Promise<SubtitleScanStatus> {
  return apiFetch<SubtitleScanStatus>("/api/admin/subtitle-scan", { cache: "no-store", signal });
}

export async function startSubtitleScan(): Promise<SubtitleScanStatus> {
  return apiFetch<SubtitleScanStatus>("/api/admin/subtitle-scan", { method: "POST" });
}

export async function getSubtitleExtractAll(signal?: AbortSignal): Promise<SubtitleExtractStatus> {
  return apiFetch<SubtitleExtractStatus>("/api/admin/subtitle-extract-all", { cache: "no-store", signal });
}

export async function startSubtitleExtractAll(): Promise<SubtitleExtractStatus> {
  return apiFetch<SubtitleExtractStatus>("/api/admin/subtitle-extract-all", { method: "POST" });
}
