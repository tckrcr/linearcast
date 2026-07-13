import type {
  FillerAssetCandidateList,
  MediaPackageCandidate,
  MediaPackageCandidateList,
  PackageProfile,
} from "../types";
import { apiFetch } from "./client";
import type { MediaMovie, MediaSearchResult, MusicArtist } from "./media";

export type ScheduleBuilderSourceStatus = {
  hasMediaSource: boolean;
  plexConfigured: boolean;
  jellyfinConfigured: boolean;
  localSourceCount: number;
};

export type ScheduleBuilderCreateChannelResponse = {
  channelID: string;
  displayName: string;
  created: boolean;
  syncedMedia: number;
  scheduleEntries: number;
  profile: string;
  queued?: string[];
  alreadyPending?: string[];
  alreadyReady?: string[];
  failed?: Array<{ mediaId: string; code: string; message: string }>;
};

export function getScheduleBuilderSourceStatus(): Promise<ScheduleBuilderSourceStatus> {
  return apiFetch<ScheduleBuilderSourceStatus>("/api/admin/media-sources/status", { cache: "no-store" });
}

export async function getScheduleBuilderProfileList(): Promise<{
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

export function getScheduleBuilderCandidates(
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

// getAllScheduleBuilderCandidates pulls the full candidate list for a profile
// by paging through the server's 100-row limit. List-import matches against the
// whole library, not the debounced search view, so it needs every row.
export async function getAllScheduleBuilderCandidates(
  profile: string,
  status = "all",
): Promise<MediaPackageCandidate[]> {
  const pageSize = 100;
  const all: MediaPackageCandidate[] = [];
  for (let offset = 0; ; offset += pageSize) {
    const page = await getScheduleBuilderCandidates(profile, "", status, offset);
    all.push(...page.media);
    if (page.media.length < pageSize || all.length >= page.count) break;
  }
  return all;
}

export async function getScheduleBuilderMovies(): Promise<MediaMovie[]> {
  const body = await apiFetch<{ movies: MediaMovie[] } | null>("/api/media/movies", { cache: "no-store" });
  return body?.movies ?? [];
}

export async function getScheduleBuilderAlbums(): Promise<MusicArtist[]> {
  const body = await apiFetch<{ artists: MusicArtist[] } | null>("/api/media/albums", { cache: "no-store" });
  return body?.artists ?? [];
}

export async function getScheduleBuilderGroup(group: string): Promise<MediaSearchResult[]> {
  const body = await apiFetch<MediaSearchResult[] | null>("/api/media/by-group", {
    cache: "no-store",
    query: { group },
  });
  return body ?? [];
}

export function getScheduleBuilderFillerCandidates(
  profile?: string,
  signal?: AbortSignal,
): Promise<FillerAssetCandidateList> {
  return apiFetch<FillerAssetCandidateList>("/api/filler-assets/candidates", {
    cache: "no-store",
    signal,
    query: { profile },
  });
}

// One client-composed schedule row. start is implied by contiguous laying, so
// only the media, its play offset, and its on-air duration are sent.
export type ScheduleBuilderEntryInput = {
  mediaId: string;
  offsetMs: number;
  durationMs: number;
};

export function createScheduleBuilderChannel(req: {
  displayName: string;
  packageProfile: string;
  playbackMode?: "packaged";
  mediaIds: string[];
  ordering?: string;
  scheduleMode?: "back_to_back" | "slot_grid" | string;
  slotDurationMs?: number;
  // "on_demand" defers packaging until a viewer tunes in; omit/"eager" packages
  // the whole channel ahead.
  prefillMode?: "eager" | "on_demand";
  adaptiveBitrate?: string;
  // For slot-grid the client sends the full gap-free layout (primary + filler)
  // so the server never builds — or persists — a schedule with holes.
  entries?: ScheduleBuilderEntryInput[];
  fillerMediaIds?: string[];
}): Promise<ScheduleBuilderCreateChannelResponse> {
  return apiFetch<ScheduleBuilderCreateChannelResponse>("/api/channels", {
    method: "POST",
    json: req,
  });
}
