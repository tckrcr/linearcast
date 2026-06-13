import type {
  FillerAssetCandidateList,
  MediaPackageCandidateList,
  PackageProfile,
} from "../types";
import { apiFetch } from "./client";
import type { MediaSearchResult, MediaShow, MusicArtist } from "./media";

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
  queued: string[];
  alreadyPending: string[];
  alreadyReady: string[];
  failed: Array<{ mediaId: string; code: string; message: string }>;
};

export function getScheduleBuilderSourceStatus(): Promise<ScheduleBuilderSourceStatus> {
  return apiFetch<ScheduleBuilderSourceStatus>("/api/schedule-builder/source-status", { cache: "no-store" });
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
  } | null>("/api/schedule-builder/package-profiles", { cache: "no-store" });
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
  return apiFetch<MediaPackageCandidateList>("/api/schedule-builder/package-candidates", {
    cache: "no-store",
    signal,
    query: { profile, search, status, offset: offset != null && offset > 0 ? String(offset) : undefined },
  });
}

export async function getScheduleBuilderShows(): Promise<MediaShow[]> {
  const body = await apiFetch<{ shows: MediaShow[] } | null>("/api/schedule-builder/shows", { cache: "no-store" });
  return body?.shows ?? [];
}

export async function getScheduleBuilderAlbums(): Promise<MusicArtist[]> {
  const body = await apiFetch<{ artists: MusicArtist[] } | null>("/api/schedule-builder/albums", { cache: "no-store" });
  return body?.artists ?? [];
}

export async function getScheduleBuilderGroup(group: string): Promise<MediaSearchResult[]> {
  const body = await apiFetch<MediaSearchResult[] | null>("/api/schedule-builder/by-group", {
    cache: "no-store",
    query: { group },
  });
  return body ?? [];
}

export function getScheduleBuilderFillerCandidates(
  profile?: string,
  signal?: AbortSignal,
): Promise<FillerAssetCandidateList> {
  return apiFetch<FillerAssetCandidateList>("/api/schedule-builder/filler-candidates", {
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
  playbackMode?: "packaged" | "plex_relay";
  mediaIds: string[];
  ordering?: string;
  scheduleMode?: "back_to_back" | "slot_grid" | string;
  slotDurationMs?: number;
  // "on_demand" defers packaging until a viewer tunes in; omit/"eager" packages
  // the whole channel ahead as usual.
  prefillMode?: "eager" | "on_demand";
  adaptiveBitrate?: string;
  // For slot-grid the client sends the full gap-free layout (primary + filler)
  // so the server never builds — or persists — a schedule with holes.
  entries?: ScheduleBuilderEntryInput[];
  fillerMediaIds?: string[];
}): Promise<ScheduleBuilderCreateChannelResponse> {
  return apiFetch<ScheduleBuilderCreateChannelResponse>("/api/schedule-builder/channels", {
    method: "POST",
    json: req,
  });
}
