import type {
  LocalMediaSource,
  MediaPackageCancelResult,
  MediaPackageCandidateList,
  MediaPackageRequestResult,
  PackageProfile,
} from "../types";
import { apiFetch } from "./client";

export type MediaSearchResult = {
  mediaId: string;
  title: string;
  path: string;
  schedulingGroup: string;
  durationMs: number;
  codecCheckPassed: boolean;
};

export async function getMediaGroups(): Promise<string[]> {
  const body = await apiFetch<{ groups: string[] } | null>("/api/media/groups", { cache: "no-store" });
  return body?.groups ?? [];
}

export type MediaShowHalf = {
  half: number;
  group: string;
  episodeCount: number;
  durationMs: number;
};

export type MediaShowSeason = {
  season: number;
  episodeCount: number;
  durationMs: number;
  halves: MediaShowHalf[];
};

export type MediaShow = {
  showName: string;
  episodeCount: number;
  durationMs: number;
  seasonCount: number;
  seasons: MediaShowSeason[];
};

export async function getMediaShows(): Promise<MediaShow[]> {
  const body = await apiFetch<{ shows: MediaShow[] } | null>("/api/media/shows", { cache: "no-store" });
  return body?.shows ?? [];
}

export type MusicAlbum = {
  albumName: string;
  group: string;
  trackCount: number;
  durationMs: number;
};

export type MusicArtist = {
  artistName: string;
  albumCount: number;
  trackCount: number;
  durationMs: number;
  albums: MusicAlbum[];
};

export async function getMediaAlbums(): Promise<MusicArtist[]> {
  const body = await apiFetch<{ artists: MusicArtist[] } | null>("/api/media/albums", { cache: "no-store" });
  return body?.artists ?? [];
}

export async function getMediaByGroup(group: string): Promise<MediaSearchResult[]> {
  const body = await apiFetch<MediaSearchResult[] | null>("/api/media/by-group", {
    cache: "no-store",
    query: { group },
  });
  return body ?? [];
}

export async function searchMedia(q: string, channelId?: string): Promise<MediaSearchResult[]> {
  const body = await apiFetch<MediaSearchResult[] | null>("/api/media", {
    cache: "no-store",
    query: { q, channelId },
  });
  return body ?? [];
}

export async function getMediaPackageCandidates(
  profile?: string,
  search?: string,
  status?: string,
  offset?: number,
  signal?: AbortSignal,
): Promise<MediaPackageCandidateList> {
  return apiFetch<MediaPackageCandidateList>("/api/media/package-candidates", {
    cache: "no-store",
    signal,
    query: { profile, search, status, offset: offset != null && offset > 0 ? String(offset) : undefined },
  });
}

export async function getMediaPackageProfiles(): Promise<string[]> {
  return (await getMediaPackageProfileList()).profiles;
}

export async function getMediaPackageProfileList(): Promise<{
  profiles: string[];
  profileDetails: PackageProfile[];
  defaultProfile: string;
}> {
  const body = await apiFetch<{
    profiles?: string[];
    profileDetails?: PackageProfile[];
    defaultProfile?: string;
  } | null>("/api/media/package-profiles", { cache: "no-store" });
  return {
    profiles: body?.profiles ?? [],
    profileDetails: body?.profileDetails ?? [],
    defaultProfile: body?.defaultProfile ?? body?.profiles?.[0] ?? "",
  };
}

export async function requestMediaPackages(
  mediaIds: string[],
  profile?: string,
): Promise<MediaPackageRequestResult> {
  return apiFetch<MediaPackageRequestResult>("/api/media/package", {
    method: "POST",
    json: { mediaIds, ...(profile ? { profile } : {}) },
  });
}

export async function cancelMediaPackages(input: {
  mediaIds?: string[];
  profile?: string;
  all?: boolean;
}): Promise<MediaPackageCancelResult> {
  return apiFetch<MediaPackageCancelResult>("/api/media/package/cancel", {
    method: "POST",
    json: input,
  });
}

export type FsBrowseResult = {
  root: string;
  path: string;
  dirs: { name: string; path: string; mediaCount: number }[];
};

export async function browseFs(path?: string): Promise<FsBrowseResult> {
  return apiFetch<FsBrowseResult>("/api/fs/browse", {
    cache: "no-store",
    query: path ? { path } : undefined,
  });
}

export type IngestJob = {
  status: "running" | "done" | "failed" | "cancelled";
  logPath?: string;
  processed?: number;
  total?: number;
  summary?: { total: number; passed: number; failed: number; failuresByReason?: Record<string, number> };
  error?: string;
};

export async function startIngest(path: string): Promise<{ jobId: string }> {
  return apiFetch<{ jobId: string }>("/api/ingest", {
    method: "POST",
    json: { path },
  });
}

export async function pollIngest(jobId: string, signal?: AbortSignal): Promise<IngestJob> {
  return apiFetch<IngestJob>(`/api/ingest/${encodeURIComponent(jobId)}`, {
    cache: "no-store",
    signal,
  });
}

export async function cancelIngest(jobId: string): Promise<{ ok: boolean }> {
  return apiFetch<{ ok: boolean }>(`/api/ingest/${encodeURIComponent(jobId)}/cancel`, {
    method: "POST",
  });
}

export async function getLocalMediaSources(): Promise<LocalMediaSource[]> {
  const body = await apiFetch<{ sources?: LocalMediaSource[] } | null>("/api/admin/local-sources", {
    cache: "no-store",
  });
  return body?.sources ?? [];
}

export async function saveLocalMediaSource(input: {
  id?: string;
  name: string;
  mediaKind: LocalMediaSource["mediaKind"] | "tv";
  paths: string[];
}): Promise<LocalMediaSource> {
  const path = input.id
    ? `/api/admin/local-sources/${encodeURIComponent(input.id)}`
    : "/api/admin/local-sources";
  return apiFetch<LocalMediaSource>(path, {
    method: input.id ? "PUT" : "POST",
    json: { name: input.name, mediaKind: input.mediaKind, paths: input.paths },
  });
}

export async function deleteLocalMediaSource(id: string): Promise<{ deleted: boolean }> {
  return apiFetch<{ deleted: boolean }>(`/api/admin/local-sources/${encodeURIComponent(id)}`, {
    method: "DELETE",
  });
}

export async function startLocalMediaSourceScan(id: string): Promise<{ jobId: string }> {
  return apiFetch<{ jobId: string }>(`/api/admin/local-sources/${encodeURIComponent(id)}/scan`, {
    method: "POST",
  });
}

export async function upsertPackageProfile(profile: PackageProfile): Promise<{ name: string; status: string }> {
  return apiFetch<{ name: string; status: string }>(`/api/package-profiles/${encodeURIComponent(profile.name)}`, {
    method: "PUT",
    json: profile,
  });
}

export async function deletePackageProfile(name: string): Promise<{ name: string; deleted: boolean; disabled?: boolean }> {
  return apiFetch<{ name: string; deleted: boolean; disabled?: boolean }>(`/api/package-profiles/${encodeURIComponent(name)}`, {
    method: "DELETE",
  });
}

export async function setDefaultPackagedProfile(name: string): Promise<{ ok: boolean; default: string }> {
  return apiFetch<{ ok: boolean; default: string }>("/api/admin/default-packaged-profile", {
    method: "PUT",
    json: { name },
  });
}

export type SubtitleExtractResult = {
  embeddedExtracted: boolean;
  skipped: boolean;
};

export async function extractMediaSubtitles(mediaId: string): Promise<SubtitleExtractResult> {
  return apiFetch<SubtitleExtractResult>(`/api/media/${encodeURIComponent(mediaId)}/subtitles/extract`, {
    method: "POST",
  });
}

export type SubtitleTrack = {
  language: string;
  source: string;
  codec: string;
  hasFile: boolean;
};

export async function getMediaSubtitles(mediaId: string): Promise<SubtitleTrack[]> {
  return apiFetch<SubtitleTrack[]>(`/api/media/${encodeURIComponent(mediaId)}/subtitles`, {
    cache: "no-store",
  });
}

export async function deleteMediaSubtitle(mediaId: string, language: string): Promise<{ deleted: boolean }> {
  return apiFetch<{ deleted: boolean }>(`/api/media/${encodeURIComponent(mediaId)}/subtitles/${encodeURIComponent(language)}`, {
    method: "DELETE",
  });
}
