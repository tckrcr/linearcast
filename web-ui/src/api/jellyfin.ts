import type { JellyfinStatus, MediaLibrary } from "../types";
import { apiFetch } from "./client";

export async function getJellyfinStatus() {
  return apiFetch<JellyfinStatus>("/api/admin/jellyfin/status", { cache: "no-store" });
}

export async function setJellyfinConfig(url: string, apiKey: string, pathMap?: string) {
  return apiFetch<JellyfinStatus>("/api/admin/jellyfin/config", {
    method: "PUT",
    json: { url, apiKey, pathMap: pathMap ?? "" },
  });
}

export async function clearJellyfinConfig() {
  return apiFetch<JellyfinStatus>("/api/admin/jellyfin/config", {
    method: "DELETE",
  });
}

export async function getJellyfinLibraries(): Promise<MediaLibrary[]> {
  return apiFetch<MediaLibrary[]>("/api/admin/jellyfin/libraries", { cache: "no-store" });
}

export async function startJellyfinScan(libraryId: string, maxResolution: string): Promise<{ jobId: string }> {
  return apiFetch<{ jobId: string }>("/api/admin/jellyfin/scan", {
    method: "POST",
    json: { libraryId, maxResolution },
  });
}
