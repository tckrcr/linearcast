import type {
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

export function createScheduleBuilderChannel(req: {
  displayName: string;
  packageProfile: string;
  mediaIds: string[];
  ordering?: string;
}): Promise<ScheduleBuilderCreateChannelResponse> {
  return apiFetch<ScheduleBuilderCreateChannelResponse>("/api/schedule-builder/channels", {
    method: "POST",
    json: req,
  });
}
