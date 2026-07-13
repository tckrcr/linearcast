import type { MediaLibrary, PlexPinPoll, PlexPinStart, PlexStatus } from "../types";
import { apiFetch } from "./client";

export async function getPlexStatus() {
  return apiFetch<PlexStatus>("/api/admin/plex/status", { cache: "no-store" });
}

export async function startPlexPin() {
  return apiFetch<PlexPinStart>("/api/admin/plex/pin", { method: "POST" });
}

export async function pollPlexPin(id: number, code: string, signal?: AbortSignal) {
  return apiFetch<PlexPinPoll>(`/api/admin/plex/pin/${id}?code=${encodeURIComponent(code)}`, { cache: "no-store", signal });
}

export async function setPlexConfig(url: string, token: string, pathMap?: string) {
  return apiFetch<PlexStatus>("/api/admin/plex/config", {
    method: "PUT",
    json: { url, token, pathMap: pathMap ?? "" },
  });
}

export async function clearPlexConfig() {
  return apiFetch<PlexStatus>("/api/admin/plex/config", { method: "DELETE" });
}

export async function getPlexLibraries(): Promise<MediaLibrary[]> {
  return apiFetch<MediaLibrary[]>("/api/admin/plex/libraries", { cache: "no-store" });
}

export async function startPlexScan(libraryKey: string, maxResolution: string): Promise<{ jobId: string }> {
  return apiFetch<{ jobId: string }>("/api/admin/plex/scan", {
    method: "POST",
    json: { libraryKey, maxResolution },
  });
}
