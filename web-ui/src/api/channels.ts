import type {
  ChannelCloneResponse,
  ChannelFillerAssetList,
  ChannelMediaList,
  ChannelPolicy,
  ChannelSchedule,
  ChannelSchedulePreview,
  EncodeReclaimResponse,
  GuideResponse,
  SpotifyUrl,
} from "../types";
import { apiFetch, channelPath } from "./client";

// getGuide fetches the viewer-safe EPG (all guide channels + a trimmed
// schedule window) in a single request.
export async function getGuide(fromMs: number, hours = 24, signal?: AbortSignal) {
  return apiFetch<GuideResponse>("/api/guide", {
    cache: "no-store",
    signal,
    query: { from: fromMs, hours },
  });
}

export async function createChannel(req: {
  displayName: string;
  packageProfile: string;
  mediaIds: string[];
  ordering?: string;
  scheduleMode?: "back_to_back" | "slot_grid" | string;
  slotDurationMs?: number;
}) {
  return apiFetch<{
    channelID: string;
    displayName: string;
    created: boolean;
    syncedMedia: number;
    scheduleEntries: number;
  }>("/api/channels", { method: "POST", json: req });
}

// The Spotify URL is a singleton (one Spotify→HLS stream per account):
// getSpotifyUrl reads it, saveSpotifyUrl upserts the one URL, clearSpotifyUrl
// deletes it.
export async function getSpotifyUrl() {
  return apiFetch<SpotifyUrl>("/api/spotify-url", { cache: "no-store" });
}

export async function saveSpotifyUrl(upstreamHlsUrl: string) {
  return apiFetch<SpotifyUrl>("/api/spotify-url", {
    method: "PUT",
    json: { upstreamHlsUrl },
  });
}

export async function clearSpotifyUrl() {
  return apiFetch<{ configured: boolean; deleted: boolean }>("/api/spotify-url", {
    method: "DELETE",
  });
}

export type UpstreamProbeResult = {
  reachable: boolean;
  status?: number;
  contentType?: string;
  looksLikeHls: boolean;
  error?: string;
};

// probeUpstreamHLS asks the server to fetch the upstream once and report
// reachability. It is advisory only — saving the Spotify URL never requires the
// probe to pass.
export async function probeUpstreamHLS(upstreamHlsUrl: string) {
  return apiFetch<UpstreamProbeResult>("/api/channels/probe-upstream", {
    method: "POST",
    json: { upstreamHlsUrl },
  });
}

// describeProbeResult turns a raw probe into a short message and an ok flag.
// ok=false is an advisory warning (reachability/format), not a hard failure.
export function describeProbeResult(r: UpstreamProbeResult): { ok: boolean; text: string } {
  if (!r.reachable) {
    return { ok: false, text: `Not reachable${r.error ? `: ${r.error}` : ""}` };
  }
  const status = r.status ?? 200;
  if (status < 200 || status >= 400) {
    return { ok: false, text: `Reachable but returned HTTP ${status}` };
  }
  if (!r.looksLikeHls) {
    return { ok: false, text: `Reachable (HTTP ${status}) but does not look like an HLS playlist` };
  }
  return { ok: true, text: `Reachable — looks like HLS (HTTP ${status})` };
}

export async function extendChannel(channelID: string, hours?: number) {
  return apiFetch<{
    channelID: string;
    inserted: number;
    lastEndMs?: number;
    skippedLowWater?: boolean;
    note?: string;
  }>(channelPath(channelID, "/extend"), {
    method: "POST",
    json: hours ? { hours } : undefined,
  });
}

export async function clearChannelSchedule(channelID: string, afterMs?: number) {
  return apiFetch<{ channelID: string; cleared: number }>(
    channelPath(channelID, "/schedule"),
    { method: "DELETE", query: { after: afterMs } },
  );
}

export async function restartChannelPlayback(channelID: string) {
  return apiFetch<{
    channelID: string;
    cleared: number;
    inserted: number;
    lastEndMs?: number;
    warning?: string;
  }>(channelPath(channelID, "/restart-playback"), { method: "POST" });
}

export async function patchChannel(channelID: string, fields: { enabled?: boolean; hiddenFromGuide?: boolean }) {
  return apiFetch<{
    id: string;
    displayName: string;
    enabled: boolean;
    hiddenFromGuide: boolean;
  }>(channelPath(channelID), { method: "PATCH", json: fields });
}

export async function stopChannelEncoder(channelID: string) {
  return apiFetch<{ channelID: string; note: string }>(
    channelPath(channelID, "/stop-encoder"),
    { method: "POST" },
  );
}

export async function deleteChannel(
  channelID: string,
  opts: { reclaimEncodes?: boolean; force?: boolean } = {},
) {
  return apiFetch<{
    channelID: string;
    deleted: boolean;
    note?: string;
    reclaim?: EncodeReclaimResponse;
  }>(channelPath(channelID), {
    method: "DELETE",
    query: {
      "reclaim-encodes": opts.reclaimEncodes || undefined,
      force: opts.force || undefined,
    },
  });
}

export async function cloneChannel(channelID: string) {
  return apiFetch<ChannelCloneResponse>(channelPath(channelID, "/clone"), {
    method: "POST",
  });
}

export async function updateChannelOnDemandProfile(channelID: string, profile: string) {
  return apiFetch<ChannelPolicy>(channelPath(channelID, "/on-demand-profile"), {
    method: "PUT",
    json: { profile },
  });
}

export async function getChannelArtwork(channelID: string) {
  return apiFetch<{ channelId: string; artworkUrl?: string }>(
    channelPath(channelID, "/artwork"),
    { cache: "no-store" },
  );
}

export async function updateChannelArtwork(channelID: string, artworkUrl: string) {
  return apiFetch<{ channelId: string; artworkUrl?: string }>(
    channelPath(channelID, "/artwork"),
    { method: "PUT", json: { artworkUrl } },
  );
}

export async function updateChannelUpstreamHLS(channelID: string, upstreamHlsUrl: string) {
  return apiFetch<void>(channelPath(channelID, "/upstream-hls"), {
    method: "PUT",
    json: { upstreamHlsUrl },
  });
}

export async function resetChannelArtwork(channelID: string) {
  return apiFetch<{ channelId: string; artworkUrl?: string }>(
    channelPath(channelID, "/artwork"),
    { method: "DELETE" },
  );
}

export async function getChannelSchedule(channelID: string, fromMs: number, hours = 24, signal?: AbortSignal) {
  return apiFetch<ChannelSchedule>(channelPath(channelID, "/schedule"), {
    cache: "no-store",
    signal,
    query: { from: fromMs, hours },
  });
}

export async function getChannelSchedulePreview(channelID: string, fromMs: number, hours = 24) {
  return apiFetch<ChannelSchedulePreview>(channelPath(channelID, "/schedule/preview"), {
    cache: "no-store",
    query: { from: fromMs, hours },
  });
}

export async function getChannelMedia(channelID: string) {
  return apiFetch<ChannelMediaList>(channelPath(channelID, "/media"), {
    cache: "no-store",
  });
}

export async function getChannelFillerAssets(channelID: string) {
  return apiFetch<ChannelFillerAssetList>(channelPath(channelID, "/filler-assets"), {
    cache: "no-store",
  });
}

export async function fillScheduleGap(
  channelID: string,
  mediaId: string,
  startMs: number,
  offsetMs = 0,
  offsetMode?: "zero" | "sequential",
) {
  return apiFetch<{
    channelID: string;
    entryId: string;
    startMs: number;
    endMs: number;
    mediaId: string;
    durationMs: number;
    offsetMs: number;
    packageProfile: string;
  }>(channelPath(channelID, "/schedule/gaps/fill"), {
    method: "POST",
    json: { mediaId, startMs, offsetMs, ...(offsetMode ? { offsetMode } : {}) },
  });
}

// recomposeSlotGridSchedule rebuilds a slot-grid channel's future schedule
// gap-free in one shot (clear-after-now + server-side slot tiling). Replaces
// the per-gap fillScheduleGap editing flow for existing slot-grid channels.
export async function recomposeSlotGridSchedule(channelID: string) {
  return apiFetch<{
    channelID: string;
    fromMs: number;
    cleared: number;
    inserted: number;
    lastEndMs: number;
    gappy: boolean;
    note?: string;
  }>(channelPath(channelID, "/schedule/recompose"), {
    method: "POST",
  });
}

export async function upsertScheduleEntry(
  channelID: string,
  mediaId: string,
  startMs: number,
) {
  return apiFetch<{
    channelID: string;
    startMs: number;
    mediaId: string;
    durationMs: number;
    cleared: number;
    inserted: number;
    lastEndMs?: number;
    note?: string;
  }>(channelPath(channelID, "/schedule/entries"), {
    method: "POST",
    json: { mediaId, startMs },
  });
}

export async function insertScheduleEntryAfter(
  channelID: string,
  entryId: string,
  mediaId: string,
) {
  return apiFetch<{
    channelID: string;
    entryId: string;
    afterEntryId: string;
    mediaId: string;
    startMs: number;
    durationMs: number;
    inserted: number;
  }>(channelPath(channelID, `/schedule/entries/${encodeURIComponent(entryId)}/after`), {
    method: "POST",
    json: { mediaId },
  });
}

export async function insertScheduleEntryBefore(
  channelID: string,
  entryId: string,
  mediaId: string,
) {
  return apiFetch<{
    channelID: string;
    entryId: string;
    beforeEntryId: string;
    mediaId: string;
    startMs: number;
    durationMs: number;
    inserted: number;
  }>(channelPath(channelID, `/schedule/entries/${encodeURIComponent(entryId)}/before`), {
    method: "POST",
    json: { mediaId },
  });
}

export async function deleteScheduleEntry(channelID: string, entryId: string) {
  return apiFetch<{ channelID: string; entryId: string; inserted: number }>(
    channelPath(channelID, `/schedule/entries/${entryId}`),
    { method: "DELETE", cache: "no-store", query: { rebuild: false } },
  );
}

export async function deleteScheduleRange(channelID: string, fromMs: number, toMs: number) {
  return apiFetch<{
    channelID: string;
    fromMs: number;
    toMs: number;
    rebuildStartMs?: number;
    deleted: number;
    inserted: number;
    lastEndMs?: number;
    note?: string;
  }>(channelPath(channelID, "/schedule/range"), {
    method: "DELETE",
    cache: "no-store",
    query: { from: fromMs, to: toMs, rebuild: false },
  });
}

export async function saveScheduleWindowOrdered(
  channelID: string,
  req: {
    fromMs: number;
    toMs: number;
    tailMode: "preserve" | "jump";
    extendTail?: boolean;
    entries: Array<{ mediaId: string }>;
  },
) {
  return apiFetch<{
    channelID: string;
    fromMs: number;
    toMs: number;
    tailMode: "preserve" | "jump";
    extendTail: boolean;
    cleared: number;
    inserted: number;
    lastEndMs?: number;
    resumeAfterMedia?: string;
    note?: string;
  }>(channelPath(channelID, "/schedule/window/order"), {
    method: "PUT",
    json: req,
  });
}

export async function addChannelMedia(
  channelID: string,
  mediaId: string,
) {
  return apiFetch<{
    channelID: string;
    mediaId: string;
    added: boolean;
    note?: string;
  }>(channelPath(channelID, "/media"), {
    method: "POST",
    json: { mediaId },
  });
}

export async function removeChannelMedia(
  channelID: string,
  mediaId: string,
  pruneSchedule = true,
) {
  return apiFetch<{
    channelID: string;
    mediaId: string;
    removed: boolean;
    prunedSchedule: number;
    inserted: number;
    note?: string;
  }>(channelPath(channelID, `/media/${encodeURIComponent(mediaId)}`), {
    method: "DELETE",
    query: pruneSchedule ? undefined : { pruneSchedule: "false" },
  });
}

export async function reorderChannelMedia(channelID: string, order: string[]) {
  return apiFetch<{ channelID: string; count: number; note?: string }>(
    channelPath(channelID, "/media/order"),
    { method: "PUT", json: { order } },
  );
}

// moveChannelMedia repositions a single item in the channel's linked-list
// order. Pass afterMediaId === "" to move to the head.
export async function moveChannelMedia(
  channelID: string,
  mediaId: string,
  afterMediaId: string,
) {
  return apiFetch<{
    channelID: string;
    mediaId: string;
    afterMediaId: string;
    note?: string;
  }>(channelPath(channelID, `/media/${encodeURIComponent(mediaId)}/move`), {
    method: "POST",
    json: { afterMediaId },
  });
}
