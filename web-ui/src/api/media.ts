import type {
  LocalMediaSource,
  MediaPackageCancelResult,
  MediaPackageCandidateList,
  MediaPackageRequestResult,
  MediaUpdateResponse,
  PackageProfile,
} from "../types";
import { ApiError, apiFetch } from "./client";

export async function updateMediaFields(
  mediaId: string,
  fields: { title?: string; collectionName?: string; seasonNumber?: number | null; episodeNumber?: number | null },
) {
  return apiFetch<MediaUpdateResponse>(`/api/media/${encodeURIComponent(mediaId)}`, {
    method: "PATCH",
    json: fields,
  });
}

export type MediaSearchResult = {
  mediaId: string;
  title: string;
  path: string;
  collectionName: string;
  sourceRef?: string;
  durationMs: number;
  videoHeight?: number;
  videoCodec?: string;
  codecCheckPassed: boolean;
};

export type MediaInventoryItem = {
  mediaId: string;
  title: string;
  path: string;
  pathRoot: string;
  releaseGroup?: string;
  episodeCode?: string;
  seasonNumber?: number;
  episodeNumber?: number;
  collection: string;
  sourceRef?: string;
  source: string;
  mediaKind: "video" | "music" | string;
  durationMs: number;
  container: string;
  videoCodec: string;
  videoWidth?: number;
  videoHeight?: number;
  audioCodec: string;
  codecCheckPassed: boolean;
  codecCheckReason?: string;
  readyPackages: number;
  pendingPackages: number;
  processingPackages: number;
  failedPackages: number;
  packageProfiles?: string;
};

export type MediaInventoryResponse = {
  count: number;
  limit: number;
  offset: number;
  media: MediaInventoryItem[];
};

export type MediaInventoryFilters = {
  q?: string;
  title?: string;
  episode?: string;
  pathRoot?: string;
  releaseGroup?: string;
  media?: string;
  source?: string;
  kind?: string;
  collection?: string;
  packageStatus?: string;
  codecStatus?: string;
  sortBy?: string;
  sortDir?: "asc" | "desc";
  limit?: number;
  offset?: number;
};

export type MediaCollectionBulkAction = "set" | "clear" | "rename";

export type MediaCollectionBulkRequest = {
  action: MediaCollectionBulkAction;
  collection?: string;
  fromCollection?: string;
  mediaIds?: string[];
  filter?: Omit<MediaInventoryFilters, "limit" | "offset">;
  dryRun?: boolean;
};

export type MediaCollectionBulkResponse = {
  action: string;
  collection?: string;
  fromCollection?: string;
  dryRun: boolean;
  matched: number;
  updated: number;
};

export async function getMediaInventory(filters: MediaInventoryFilters = {}): Promise<MediaInventoryResponse> {
  return apiFetch<MediaInventoryResponse>("/api/media/inventory", {
    cache: "no-store",
    query: {
      q: filters.q,
      title: filters.title,
      episode: filters.episode,
      pathRoot: filters.pathRoot,
      releaseGroup: filters.releaseGroup,
      media: filters.media,
      source: filters.source,
      kind: filters.kind,
      collection: filters.collection,
      packageStatus: filters.packageStatus,
      codecStatus: filters.codecStatus,
      sortBy: filters.sortBy,
      sortDir: filters.sortDir,
      limit: filters.limit != null ? String(filters.limit) : undefined,
      offset: filters.offset != null ? String(filters.offset) : undefined,
    },
  });
}

export async function deleteMedia(mediaId: string) {
  try {
    return await apiFetch<{
      mediaId: string;
      deleted: boolean;
      blockers?: { channelId: string; displayName: string; kind: string }[];
      warnings?: string[];
      packageIds?: string[];
    }>(`/api/media/${encodeURIComponent(mediaId)}`, {
      method: "DELETE",
    });
  } catch (err) {
    if (err instanceof ApiError && err.status === 409 && err.body && typeof err.body === "object") {
      return err.body as {
        mediaId: string;
        deleted: boolean;
        blockers?: { channelId: string; displayName: string; kind: string }[];
        warnings?: string[];
        packageIds?: string[];
      };
    }
    throw err;
  }
}

export type EncodeReclaimItem = {
  mediaId: string;
  packageId: string;
  profile: string;
  status: string;
  packageRoot?: string;
  bytes?: number;
  referenced: boolean;
  skipped: boolean;
  deleted: boolean;
};

export type EncodeReclaimResponse = {
  generatedAt: string;
  dryRun: boolean;
  force: boolean;
  candidates: number;
  deletedRows: number;
  skippedRows: number;
  totalBytes: number;
  items: EncodeReclaimItem[];
  warnings?: string[];
};

export async function deleteMediaPackages(
  mediaId: string,
  profile?: string,
): Promise<EncodeReclaimResponse> {
  return apiFetch<EncodeReclaimResponse>("/api/admin/maintenance/packages", {
    method: "DELETE",
    query: {
      media: mediaId,
      "dry-run": "false",
      ...(profile ? { profile } : {}),
    },
  });
}

export async function backfillMediaOrdering() {
  return apiFetch<{
    generatedAt: string;
    scanned: number;
    updated: number;
  }>("/api/admin/maintenance/media-ordering", {
    method: "POST",
  });
}

export async function bulkUpdateMediaCollections(
  input: MediaCollectionBulkRequest,
): Promise<MediaCollectionBulkResponse> {
  return apiFetch<MediaCollectionBulkResponse>("/api/media/collections/bulk", {
    method: "POST",
    json: input,
  });
}

export async function getMediaGroups(): Promise<string[]> {
  const body = await apiFetch<{ groups: string[] } | null>("/api/media/groups", { cache: "no-store" });
  return body?.groups ?? [];
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

export type MediaMovie = {
  title: string;
  group: string;
  itemCount: number;
  durationMs: number;
};

export async function getMediaMovies(): Promise<MediaMovie[]> {
  const body = await apiFetch<{ movies: MediaMovie[] } | null>("/api/media/movies", { cache: "no-store" });
  return body?.movies ?? [];
}

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
  mediaKind: LocalMediaSource["mediaKind"] | "tv" | "filler";
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

export async function startAllLocalMediaSourcesScan(): Promise<{ jobId: string }> {
  return apiFetch<{ jobId: string }>("/api/admin/local-sources/scan", {
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

export async function enablePackageProfile(name: string): Promise<{ name: string; enabled: boolean }> {
  return apiFetch<{ name: string; enabled: boolean }>(`/api/package-profiles/${encodeURIComponent(name)}/enable`, {
    method: "POST",
  });
}

export async function setDefaultPackagedProfile(name: string): Promise<{ ok: boolean; default: string }> {
  return apiFetch<{ ok: boolean; default: string }>("/api/admin/default-packaged-profile", {
    method: "PUT",
    json: { name },
  });
}
