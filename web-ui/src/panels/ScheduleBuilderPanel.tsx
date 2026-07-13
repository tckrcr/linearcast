import { Fragment, useEffect, useMemo, useRef, useState } from "react";
import {
  alignToSlot,
  clipToGrid,
  composeSlotGridEntries,
  gapAfterByPrimary,
  type FillerMeta,
  type SlotGridComposition,
} from "./scheduleFiller";
import {
  getAllScheduleBuilderCandidates,
  getScheduleBuilderAlbums,
  getScheduleBuilderCandidates,
  getScheduleBuilderFillerCandidates,
  getScheduleBuilderGroup,
  getScheduleBuilderMovies,
  getScheduleBuilderProfileList,
  requestMediaPackages,
} from "../api";
import type { MediaMovie, MusicArtist } from "../api/media";
import { formatMs } from "../format";
import { useScheduleEditor } from "../hooks/useScheduleEditor";
import { useHasMediaSource } from "../hooks/useHasMediaSource";
import type { ChannelNow, FillerAssetCandidateItem, MediaPackageCandidate, PackageProfile, ScheduleInsertItem } from "../types";
import { MediaPickerRail } from "./MediaPickerRail";
import { SchedulePickerMusicGrid } from "./SchedulePickerMusicGrid";
import { ScheduleTimeline } from "./ScheduleTimeline";
import styles from "./ScheduleBuilderPanel.module.css";

type PickerTab = "episodes" | "movies" | "music" | "filler";
type ScheduleBatchDragPayload =
  | { kind: "group"; group: string }
  | { kind: "album"; group: string }
  | { kind: "artist"; artistName?: string };

function packageStatusLabel(candidate: MediaPackageCandidate, profileDetails: Record<string, PackageProfile>): string {
  if (candidate.packageStatus === "ready") return "";
  if (candidate.packageStatus === "missing") return "needs package";
  const profile = candidate.packageProfile || "";
  if (candidate.packageStatus === "failed") return `failed at ${profileChipLabel(profile, profileDetails)}`;
  return `${candidate.packageStatus} at ${profileChipLabel(profile, profileDetails)}`;
}

function forcedSubtitleWarningLabel(candidate: MediaPackageCandidate): string {
  const warning = candidate.subtitleWarnings?.find((w) => w.code === "forced_pgs_dropped_by_copy_profile");
  if (!warning) return "";
  const lang = warning.language || "und";
  const title = warning.title ? ` ${warning.title}` : "";
  const stream = warning.streamIndex != null ? ` stream #${warning.streamIndex}` : "";
  return `⚠ copy profile drops forced ${lang}${title} PGS${stream}`;
}

const BROWSER_HLS_COPY_BITRATE_CEILING_BPS = 40_000_000;

function sourceBitrateLabel(candidate: MediaPackageCandidate): string {
  if (!candidate.videoBitrateBps || candidate.videoBitrateBps <= 0) return "";
  return `${(candidate.videoBitrateBps / 1_000_000).toFixed(1)} Mbps source`;
}

function browserHLSBitrateWarningLabel(candidate: MediaPackageCandidate, profile?: PackageProfile): string {
  if (profile?.video.mode !== "copy") return "";
  if (!candidate.videoBitrateBps || candidate.videoBitrateBps <= BROWSER_HLS_COPY_BITRATE_CEILING_BPS) return "";
  return `over ${(BROWSER_HLS_COPY_BITRATE_CEILING_BPS / 1_000_000).toFixed(0)} Mbps browser HLS ceiling`;
}

function candidateDisabled(candidate: MediaPackageCandidate, profile?: PackageProfile): boolean {
  return browserHLSBitrateWarningLabel(candidate, profile) !== "";
}

function candidateToInsertItem(r: MediaPackageCandidate, forceReady = false): ScheduleInsertItem {
  return {
    mediaId: r.mediaId,
    title: r.title,
    path: r.path,
    collectionName: r.collectionName,
    durationMs: r.packagedDurationMs ?? r.durationMs,
    packagedDurationMs: r.packagedDurationMs,
    packageReady: forceReady || r.packageStatus === "ready",
    channelMember: false,
  };
}

// An imported-list entry: an episode/movie name plus an optional show name —
// the two fields needed to match against the library. Matches the scraper
// shim's output; bare strings and a leading "N. " rank prefix are tolerated so
// hand-written lists work too.
type ImportListItem = { show?: string; episode: string };

// Collapse case and punctuation so a dotted path ("The.Winds.of.Winter") and a
// spaced title ("The Winds of Winter") compare equal.
function normalizeForMatch(s: string): string {
  return s.toLowerCase().replace(/[^a-z0-9]+/g, " ").trim();
}

// The IMDb scrape's metaItems is a typed-but-positional array like
// ["S1.E9", "Game of Thrones", "2011–2019", "57m", "TV-MA", "TV Episode"].
// The show is the one entry that isn't a season/episode code, year(-range),
// runtime, certificate, or media-type label.
function showFromMetaItems(meta: unknown): string | undefined {
  if (!Array.isArray(meta)) return undefined;
  for (const raw of meta) {
    const m = String(raw).trim();
    if (!m) continue;
    if (/^S\d+\.E\d+$/i.test(m)) continue; // S1.E9
    if (/^\d{4}(–|-|\s)?\d{0,4}$/.test(m)) continue; // 2011 or 2011–2019
    if (/^(\d+\s*h)?\s*(\d+\s*m)?$/i.test(m) && /[hm]/i.test(m)) continue; // 57m / 1h 2m
    if (/^(TV-\w+|G|PG|PG-13|R|NC-17|NR|Not Rated|Unrated)$/i.test(m)) continue; // cert
    if (/\b(episode|series|movie|special|short|mini)\b/i.test(m)) continue; // type
    return m;
  }
  return undefined;
}

function parseImportList(text: string): ImportListItem[] {
  const data: unknown = JSON.parse(text);
  const raw = Array.isArray(data) ? data : (data as { items?: unknown } | null)?.items;
  if (!Array.isArray(raw)) throw new Error("expected a JSON array or { items: [...] }");
  const out: ImportListItem[] = [];
  for (const r of raw) {
    if (typeof r === "string") {
      const episode = r.replace(/^\s*\d+\.\s*/, "").trim();
      if (episode) out.push({ episode });
      continue;
    }
    if (!r || typeof r !== "object") continue;
    const o = r as Record<string, unknown>;
    // rawTitle ("6. Baelor") is the scrape's field; episode/title cover a
    // pre-normalized list. Strip the leading "N. " rank prefix either way.
    const episode = String(o.episode ?? o.title ?? o.rawTitle ?? "").replace(/^\s*\d+\.\s*/, "").trim();
    if (!episode) continue;
    const showRaw =
      o.show ??
      (o.series as { title?: unknown } | undefined)?.title ??
      o.series ??
      showFromMetaItems(o.metaItems);
    const show = showRaw != null ? String(showRaw).trim() : "";
    out.push({ episode, show: show || undefined });
  }
  return out;
}

const BUILDER_CANDIDATE_LIMIT = 10;
const HOUR_MS = 3600 * 1000;
const SCHEDULE_GRID_MS = 6000;
const DEFAULT_SLOT_DURATION_MS = 30 * 60 * 1000;


export function ScheduleBuilderPanel({
  existingChannel,
  onChannelImported,
  onOpenMediaSources,
}: {
  existingChannel?: ChannelNow;
  onChannelImported: (channelId: string, result: { scheduleMode?: "back_to_back" | "slot_grid" | string }) => void;
  onOpenMediaSources?: () => void;
}) {
  const sourceGate = useHasMediaSource();

  const existingMode = existingChannel != null;

  // Channel config state
  const [displayName, setDisplayName] = useState(existingChannel?.displayName ?? "");
  const [packageProfile, setPackageProfile] = useState(existingChannel?.packageProfile ?? "");
  const [scheduleMode, setScheduleMode] = useState<"back_to_back" | "slot_grid">(
    existingChannel?.scheduleMode === "slot_grid" ? "slot_grid" : "back_to_back",
  );
  const [slotDurationMs, setSlotDurationMs] = useState(existingChannel?.slotDurationMs ?? DEFAULT_SLOT_DURATION_MS);
  // Create-time only; an existing channel's prefill mode is shown read-only.
  const [prefillMode, setPrefillMode] = useState<"eager" | "on_demand">(
    existingChannel ? (existingChannel.prefillMode as "eager" | "on_demand") ?? "on_demand" : "on_demand",
  );
  const [adaptiveBitrate, setAdaptiveBitrate] = useState("");
  const defaultProfileRef = useRef("");
  const [profiles, setProfiles] = useState<string[]>([]);
  const [profileDetails, setProfileDetails] = useState<Record<string, PackageProfile>>({});

  // Picker tab state — null means no content panel is open
  const [activeTab, setActiveTab] = useState<PickerTab | null>(null);

  // Episodes-tab search state
  const [searchQuery, setSearchQuery] = useState("");
  const [searchResults, setSearchResults] = useState<MediaPackageCandidate[]>([]);
  const [searchBusy, setSearchBusy] = useState(false);
  const [searchStatus, setSearchStatus] = useState("");
  const [readyOnly, setReadyOnly] = useState(false);
  const [importBusy, setImportBusy] = useState(false);
  const importInputRef = useRef<HTMLInputElement>(null);
  const searchTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  // Movies-tab state
  const [groupBusy, setGroupBusy] = useState<string | null>(null);
  const [movies, setMovies] = useState<MediaMovie[]>([]);
  const [moviesLoaded, setMoviesLoaded] = useState(false);
  const [moviesLoading, setMoviesLoading] = useState(false);
  const [moviesError, setMoviesError] = useState("");
  const [movieFilter, setMovieFilter] = useState("");
  const [movieBusy, setMovieBusy] = useState<string | null>(null);

  // Music-tab state
  const [artists, setArtists] = useState<MusicArtist[]>([]);
  const [artistsLoaded, setArtistsLoaded] = useState(false);
  const [artistsLoading, setArtistsLoading] = useState(false);
  const [artistsError, setArtistsError] = useState("");
  const [artistFilter, setArtistFilter] = useState("");
  const [albumBusy, setAlbumBusy] = useState<string | null>(null);
  const [artistBusy, setArtistBusy] = useState<string | null>(null);
  const [selectedArtist, setSelectedArtist] = useState<MusicArtist | null>(null);

  // Filler-tab state
  const [fillerCandidates, setFillerCandidates] = useState<FillerAssetCandidateItem[]>([]);
  const [fillerLoaded, setFillerLoaded] = useState(false);
  const [fillerLoading, setFillerLoading] = useState(false);
  const [fillerError, setFillerError] = useState("");
  const [fillerQuery, setFillerQuery] = useState("");
  const [fillerEncodeBusy, setFillerEncodeBusy] = useState<Set<string>>(new Set());
  // New-channel slot-grid only: which filler clip fills the gap after each
  // episode, keyed by the episode's draftId so the choice survives reorders.
  // The schedule must be gap-free (every gap assigned) before it can be saved.
  const [gapFillerByPrimaryId, setGapFillerByPrimaryId] = useState<Map<string, string>>(new Map());

  const channelMediaKind: "video" | "music" =
    profileDetails[packageProfile]?.mediaKind === "music" ? "music" : "video";

  // Entry management via the shared hook
  const {
    scheduleDraft,
    appendDraftEntries,
    appendDraftEntry,
    recomposeSlotGrid,
    mutationBusy,
    fillerMediaIds,
    removeDraftEntry,
    clearScheduleDraft,
    moveDraftEntry,
    undoScheduleDraftChange,
    canUndoScheduleDraft,
    importDraftChannel,
    saveBusy,
    scheduleError,
    scheduleNotice,
    scheduleData,
    scheduleLoading,
    scheduleEditMode,
    beginScheduleEdit,
    saveScheduleEdit,
  } = useScheduleEditor(
    existingChannel ?? null,
    existingMode
      ? undefined
      : {
          packageProfile,
          displayName,
          scheduleMode,
          slotDurationMs,
          prefillMode,
          adaptiveBitrate: (prefillMode === "eager" && channelMediaKind === "video" && adaptiveBitrate) || undefined,
          onImported: onChannelImported,
        },
  );

  useEffect(() => {
    if (!existingChannel) return;
    setDisplayName(existingChannel.displayName);
    setPackageProfile(existingChannel.packageProfile);
    setPrefillMode((existingChannel.prefillMode as "eager" | "on_demand") ?? "on_demand");
    setScheduleMode(existingChannel.scheduleMode === "slot_grid" ? "slot_grid" : "back_to_back");
    setSlotDurationMs(existingChannel.slotDurationMs ?? DEFAULT_SLOT_DURATION_MS);
  }, [existingChannel?.id, existingChannel?.displayName, existingChannel?.packageProfile, existingChannel?.scheduleMode, existingChannel?.slotDurationMs]);

  // Profiles
  useEffect(() => {
    getScheduleBuilderProfileList()
      .then((next) => {
        const details = Object.fromEntries(next.profileDetails.map((item) => [item.name, item]));
        const selectable = next.profiles;
        defaultProfileRef.current = next.defaultProfile;
        setProfiles(selectable);
        setProfileDetails(details);
        setPackageProfile((current) => {
          if (existingMode) return existingChannel?.packageProfile ?? current;
          return selectable.includes(current) ? current : "";
        });
      })
      .catch(() => {});
  }, [existingMode, existingChannel?.packageProfile]);

  useEffect(() => {
    if (!existingMode || scheduleEditMode || scheduleLoading || !scheduleData || scheduleData.entries.length === 0) return;
    beginScheduleEdit();
  }, [existingMode, scheduleEditMode, scheduleLoading, scheduleData, beginScheduleEdit]);

  // Collapse picker when channel kind changes
  useEffect(() => {
    setActiveTab(null);
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [channelMediaKind]);

  // Lazy-load movies
  useEffect(() => {
    if (activeTab !== "movies" || moviesLoaded || moviesLoading) return;
    setMoviesLoading(true);
    setMoviesError("");
    getScheduleBuilderMovies()
      .then((m) => { setMovies(m); setMoviesLoaded(true); })
      .catch((err) => setMoviesError(err instanceof Error ? err.message : String(err)))
      .finally(() => setMoviesLoading(false));
  }, [activeTab, moviesLoaded, moviesLoading]);

  // Lazy-load music
  useEffect(() => {
    if (activeTab !== "music" || artistsLoaded || artistsLoading) return;
    setArtistsLoading(true);
    setArtistsError("");
    getScheduleBuilderAlbums()
      .then((a) => { setArtists(a); setArtistsLoaded(true); })
      .catch((err) => setArtistsError(err instanceof Error ? err.message : String(err)))
      .finally(() => setArtistsLoading(false));
  }, [activeTab, artistsLoaded, artistsLoading]);

  // Load filler when its tab opens (existing mode) or eagerly for a new
  // slot-grid channel, where the per-episode gap dropdowns need it ready.
  const wantFiller = activeTab === "filler" || (!existingMode && scheduleMode === "slot_grid" && !!packageProfile);
  useEffect(() => {
    if (!wantFiller || fillerLoaded || fillerLoading) return;
    setFillerLoading(true);
    setFillerError("");
    getScheduleBuilderFillerCandidates(packageProfile)
      .then((r) => { setFillerCandidates(r.assets); setFillerLoaded(true); })
      .catch((err) => setFillerError(err instanceof Error ? err.message : String(err)))
      .finally(() => setFillerLoading(false));
  }, [wantFiller, fillerLoaded, fillerLoading, packageProfile]);

  // Reload filler when the profile changes — package readiness is per-profile.
  useEffect(() => {
    setFillerLoaded(false);
    setFillerCandidates([]);
  }, [packageProfile]);

  // Debounced episode search — fires only when the user types, never on
  // cold-select with an empty query (avoids flooding the backend for no result).
  useEffect(() => {
    if (activeTab !== "episodes") return;
    const q = searchQuery.trim();
    if (!q) {
      setSearchResults([]);
      setSearchStatus("");
      return;
    }
    if (searchTimerRef.current) clearTimeout(searchTimerRef.current);
    searchTimerRef.current = setTimeout(() => {
      setSearchBusy(true);
      setSearchStatus("");
      getScheduleBuilderCandidates(packageProfile, q, readyOnly ? "ready" : "all")
        .then((r) => setSearchResults(r.media))
        .catch((err) => {
          setSearchResults([]);
          setSearchStatus(err instanceof Error ? err.message : String(err));
        })
        .finally(() => setSearchBusy(false));
    }, 300);
    return () => { if (searchTimerRef.current) clearTimeout(searchTimerRef.current); };
  }, [activeTab, packageProfile, searchQuery, readyOnly]);

  async function queueGroup(group: string, index?: number) {
    if (groupBusy) return;
    setGroupBusy(group);
    try {
      const media = await getScheduleBuilderGroup(group);
      const added = appendDraftEntries(media.map((m) => candidateToInsertItemFromMedia(m)), index);
      if (!displayName.trim()) setDisplayName(group);
      if (added === 0) setSearchStatus(`all episodes from "${group}" already in queue`);
    } catch (err) {
      setSearchStatus(err instanceof Error ? err.message : String(err));
    } finally {
      setGroupBusy(null);
    }
  }

  async function queueMovie(movie: MediaMovie, index?: number) {
    if (movieBusy) return;
    setMovieBusy(movie.group);
    try {
      const media = await getScheduleBuilderGroup(movie.group);
      const added = appendDraftEntries(media.map((m) => candidateToInsertItemFromMedia(m)), index);
      if (!displayName.trim()) setDisplayName(movie.title);
      if (added === 0) setSearchStatus(`"${movie.title}" is already in the queue`);
    } catch (err) {
      setSearchStatus(err instanceof Error ? err.message : String(err));
    } finally {
      setMovieBusy(null);
    }
  }

  async function queueAlbum(group: string, index?: number) {
    if (albumBusy || artistBusy) return;
    setAlbumBusy(group);
    try {
      const media = await getScheduleBuilderGroup(group);
      const added = appendDraftEntries(media.map((m) => candidateToInsertItemFromMedia(m)), index);
      if (!displayName.trim()) setDisplayName(group);
      if (added === 0) setSearchStatus(`all tracks from "${group}" already in queue`);
    } catch (err) {
      setSearchStatus(err instanceof Error ? err.message : String(err));
    } finally {
      setAlbumBusy(null);
    }
  }

  async function queueArtist(artist: MusicArtist, index?: number) {
    if (albumBusy || artistBusy) return;
    setArtistBusy(artist.artistName);
    try {
      const batches = await Promise.all(artist.albums.map((al) => getScheduleBuilderGroup(al.group)));
      const artistDisplayName = artist.artistName || "Unknown Artist";
      const added = appendDraftEntries(batches.flat().map((m) => candidateToInsertItemFromMedia(m)), index);
      if (!displayName.trim()) setDisplayName(artistDisplayName);
      if (added === 0) setSearchStatus(`all tracks from "${artistDisplayName}" already in queue`);
    } catch (err) {
      setSearchStatus(err instanceof Error ? err.message : String(err));
    } finally {
      setArtistBusy(null);
    }
  }

  // Import a scraped list: match each entry against the full candidate library
  // for the selected profile (show + episode name), then queue the hits via the
  // same appendDraftEntries path a drag-drop uses.
  async function importShowList(items: ImportListItem[]) {
    if (!packageProfile || importBusy) return;
    if (items.length === 0) {
      setSearchStatus("no entries found in imported list");
      return;
    }
    setImportBusy(true);
    setSearchStatus("");
    try {
      const all = await getAllScheduleBuilderCandidates(packageProfile);
      const haystacks = all.map((c) => normalizeForMatch(`${c.title ?? ""} ${c.path}`));
      const picked: MediaPackageCandidate[] = [];
      const unmatched: string[] = [];
      for (const it of items) {
        const ep = normalizeForMatch(it.episode);
        const show = it.show ? normalizeForMatch(it.show) : "";
        const i = ep ? haystacks.findIndex((h) => h.includes(ep) && (!show || h.includes(show))) : -1;
        if (i >= 0) picked.push(all[i]);
        else unmatched.push(it.show ? `${it.show} — ${it.episode}` : it.episode);
      }
      const added = appendDraftEntries(picked.map((m) => candidateToInsertItemFromMedia(m)));
      if (!displayName.trim() && items[0]?.show) setDisplayName(items[0].show);
      const note = `imported ${added} of ${items.length}`;
      setSearchStatus(
        unmatched.length
          ? `${note} · no match: ${unmatched.slice(0, 4).join(", ")}${unmatched.length > 4 ? ` +${unmatched.length - 4} more` : ""}`
          : note,
      );
    } catch (err) {
      setSearchStatus(err instanceof Error ? err.message : String(err));
    } finally {
      setImportBusy(false);
    }
  }

  function toggleTab(tab: PickerTab) {
    setActiveTab((prev) => (prev === tab ? null : tab));
  }

  function insertMediaFromDrag(key: string, index: number) {
    const r = searchResults.find((x) => x.mediaId === key);
    if (r && !candidateDisabled(r, profileDetails[packageProfile])) appendDraftEntry(candidateToInsertItem(r), index);
  }

  function insertBatchFromDrag(payloadText: string, index: number) {
    let payload: ScheduleBatchDragPayload;
    try {
      payload = JSON.parse(payloadText) as ScheduleBatchDragPayload;
    } catch {
      setSearchStatus("could not read dragged batch");
      return;
    }
    if (payload.kind === "group") {
      void queueGroup(payload.group, index);
      return;
    }
    if (payload.kind === "album") {
      void queueAlbum(payload.group, index);
      return;
    }
    if (payload.kind === "artist") {
      const artist = artists.find((item) => item.artistName === payload.artistName);
      if (artist) void queueArtist(artist, index);
      else setSearchStatus(`artist not found: ${payload.artistName || "Unknown Artist"}`);
    }
  }

  const primaryTotalMs = scheduleDraft.reduce((sum, e) => sum + e.durationMs, 0);
  const validSlotDurationMs = slotDurationMs > 0 && slotDurationMs % SCHEDULE_GRID_MS === 0 ? slotDurationMs : DEFAULT_SLOT_DURATION_MS;
  const preservesExistingWallClock = existingMode && scheduleMode === "slot_grid";
  const timelineEntries = preservesExistingWallClock
    ? scheduleDraft
    : scheduleDraft.reduce<typeof scheduleDraft>((entries, entry) => {
        const rawStartMs = entries.length === 0 ? 0 : entries[entries.length - 1].endMs;
        const startMs = scheduleMode === "slot_grid" ? alignToSlot(rawStartMs, validSlotDurationMs) : rawStartMs;
        entries.push({ ...entry, startMs, endMs: startMs + entry.durationMs });
        return entries;
      }, []);
  const timelineStartMs = preservesExistingWallClock && timelineEntries.length > 0
    ? timelineEntries[0].startMs - (timelineEntries[0].startMs % HOUR_MS)
    : 0;
  const timelineEndMs = timelineEntries.length === 0 ? 0 : timelineEntries[timelineEntries.length - 1].endMs;
  const renderedTimelineMs = Math.max(0, timelineEndMs - timelineStartMs);
  const gapTotalMs = Math.max(0, renderedTimelineMs - primaryTotalMs);
  const totalMs = scheduleMode === "slot_grid" ? renderedTimelineMs : primaryTotalMs;
  const timelineWindowHours = Math.max(1, Math.ceil(Math.max(totalMs, 1) / HOUR_MS));
  // Filler assets are exempt from picker de-duplication: the same filler clip
  // can be dropped into many gaps, so placing it once must not remove it from
  // the picker the way a scheduled primary episode is removed.
  const selectedMediaKeys = new Set(
    scheduleDraft
      .filter((e) => !fillerMediaIds.has(e.mediaId))
      .flatMap((e) => [e.mediaId, e.path].filter(Boolean) as string[]),
  );
  const allKnownReady = scheduleDraft.length > 0 && scheduleDraft.every((e) => e.needsPackage !== true);

  // New-channel slot-grid composition: each episode's trailing gap is filled by
  // the filler the user picked for it, so the schedule is gap-free before save.
  // back-to-back and the existing-channel edit path don't use this.
  const isNewSlotGrid = !existingMode && scheduleMode === "slot_grid";
  const readyFiller = useMemo<FillerMeta[]>(
    () =>
      fillerCandidates
        .filter((c) => c.packageReady)
        .map((c) => ({ mediaId: c.mediaId, packagedDurationMs: clipToGrid(c.packagedDurationMs ?? c.durationMs), title: c.label }))
        .filter((f) => f.packagedDurationMs > 0),
    [fillerCandidates],
  );
  const fillerById = useMemo(() => new Map(readyFiller.map((f) => [f.mediaId, f])), [readyFiller]);
  // Gap (ms) after each episode, keyed by draftId — drives which rows get a
  // filler dropdown and which clips are long enough for that gap.
  const gapAfter = useMemo(
    () =>
      isNewSlotGrid
        ? gapAfterByPrimary(scheduleDraft.map((e) => ({ draftId: e.draftId, durationMs: e.durationMs })), validSlotDurationMs)
        : new Map<string, number>(),
    [isNewSlotGrid, scheduleDraft, validSlotDurationMs],
  );
  const slotGridFill = useMemo<SlotGridComposition | null>(() => {
    if (!isNewSlotGrid) return null;
    return composeSlotGridEntries(
      scheduleDraft.map((e) => ({ draftId: e.draftId, mediaId: e.mediaId, durationMs: e.durationMs })),
      validSlotDurationMs,
      gapFillerByPrimaryId,
      fillerById,
    );
  }, [isNewSlotGrid, scheduleDraft, validSlotDurationMs, gapFillerByPrimaryId, fillerById]);
  const slotGapsRemain = slotGridFill != null && slotGridFill.unfilledGapCount > 0;
  // gap startMs (timeline coords) -> filler title, for rendering filled gaps in
  // the draft timeline. A gap starts where its preceding episode ends.
  const filledGaps = isNewSlotGrid
    ? new Map<number, string>(
        timelineEntries.flatMap((e) => {
          const fillerId = gapFillerByPrimaryId.get(e.draftId);
          const gapMs = gapAfter.get(e.draftId) ?? 0;
          const meta = fillerId ? fillerById.get(fillerId) : undefined;
          return meta && gapMs > 0 && meta.packagedDurationMs >= gapMs
            ? [[e.endMs, meta.title] as [number, string]]
            : [];
        }),
      )
    : undefined;

  function assignGapFiller(draftId: string, fillerMediaId: string) {
    setGapFillerByPrimaryId((prev) => {
      const next = new Map(prev);
      if (fillerMediaId) next.set(draftId, fillerMediaId);
      else next.delete(draftId);
      return next;
    });
  }

  async function queueFillerPackage(mediaId: string) {
    const c = fillerCandidates.find((c) => c.mediaId === mediaId);
    if (!c || c.packageReady || fillerEncodeBusy.has(mediaId) || !packageProfile) return;
    setFillerEncodeBusy((prev) => new Set(prev).add(mediaId));
    try {
      await requestMediaPackages([mediaId], packageProfile);
      setFillerLoaded(false);
      setFillerCandidates([]);
    } catch (err) {
      setFillerError(err instanceof Error ? err.message : String(err));
    } finally {
      setFillerEncodeBusy((prev) => {
        const next = new Set(prev);
        next.delete(mediaId);
        return next;
      });
    }
  }

  async function queueAllMissingFiller() {
    const missing = fillerCandidates.filter(
      (c) => !c.packageReady && c.packageStatus === "missing" && !fillerEncodeBusy.has(c.mediaId),
    );
    if (missing.length === 0 || !packageProfile) return;
    setFillerEncodeBusy((prev) => new Set([...prev, ...missing.map((c) => c.mediaId)]));
    try {
      await requestMediaPackages(
        missing.map((c) => c.mediaId),
        packageProfile,
      );
      setFillerLoaded(false);
      setFillerCandidates([]);
    } catch (err) {
      setFillerError(err instanceof Error ? err.message : String(err));
    } finally {
      setFillerEncodeBusy(new Set());
    }
  }
  // Bulk convenience: assign the first long-enough ready clip to every gap that
  // is not already filled.
  function fillRemainingGaps() {
    setGapFillerByPrimaryId((prev) => {
      const next = new Map(prev);
      for (const [draftId, gapMs] of gapAfter) {
        if (gapMs <= 0) continue;
        const current = next.get(draftId);
        const currentOk = current && (fillerById.get(current)?.packagedDurationMs ?? 0) >= gapMs;
        if (currentOk) continue;
        const pick = readyFiller.find((f) => f.packagedDurationMs >= gapMs);
        if (pick) next.set(draftId, pick.mediaId);
      }
      return next;
    });
  }

  const importButtonLabel = existingMode
    ? saveBusy
      ? "Saving..."
      : "Save schedule"
    : saveBusy
      ? "Importing..."
      : allKnownReady
        ? "Create channel"
        : "Create channel and queue packages";
  const statusMessage = scheduleError || scheduleNotice;

  // ---------------------------------------------------------------------------
  // Source gate
  // ---------------------------------------------------------------------------

  if (!existingMode && sourceGate.loading) {
    return (
      <div className="admin-panel">
        <section className="admin-panel-section">
          <h2>Schedule builder</h2>
          <p className="muted">checking media sources...</p>
        </section>
      </div>
    );
  }

  if (!existingMode && !sourceGate.hasMediaSource) {
    return (
      <div className="admin-panel">
        <section className="admin-panel-section">
          <h2>Schedule builder</h2>
          <p className="section-purpose">
            Connect at least one media source before building a schedule.
          </p>
          <div className={styles["sb-picker-actions"]}>
            {onOpenMediaSources && (
              <button type="button" className="primary" onClick={onOpenMediaSources}>
                Open media sources
              </button>
            )}
            <button type="button" className="primary" onClick={sourceGate.refresh}>
              Recheck sources
            </button>
            {sourceGate.error && <span className="muted">{sourceGate.error}</span>}
          </div>
        </section>
      </div>
    );
  }

  // ---------------------------------------------------------------------------
  // Picker content builders
  // ---------------------------------------------------------------------------

  // Drop episodes already in the queue so the picker only shows addable rows.
  const unaddedResults = searchResults.filter(
    (r) => !(selectedMediaKeys.has(r.mediaId) || (r.path && selectedMediaKeys.has(r.path))),
  );
  const visibleSearchResults = unaddedResults.slice(0, BUILDER_CANDIDATE_LIMIT);
  const episodeItems = visibleSearchResults.map((r) => {
    const disabledReason = browserHLSBitrateWarningLabel(r, profileDetails[packageProfile]);
    const meta = [
      r.collectionName,
      sourceBitrateLabel(r),
      packageStatusLabel(r, profileDetails),
      forcedSubtitleWarningLabel(r),
      disabledReason,
    ].filter(Boolean).join(" · ");
    return {
      key: r.mediaId,
      title: r.title || r.path.split("/").pop() || r.path,
      meta: meta || undefined,
      durationMs: r.packagedDurationMs ?? r.durationMs,
      disabled: disabledReason !== "",
      actionLabel: disabledReason ? "Blocked" : undefined,
    };
  });

  // ---------------------------------------------------------------------------
  // Render
  // ---------------------------------------------------------------------------

  return (
    <div className="admin-panel sb-panel">
      <section className="admin-panel-section">
        <h2>{existingMode ? `Edit schedule: ${displayName}` : "Schedule builder"}</h2>
        <div className={styles["sb-config"]}>
          <label className={styles["sb-name-label"]}>
            <span>display name</span>
            <input
              value={displayName}
              placeholder="My Channel"
              disabled={existingMode}
              onChange={(e) => setDisplayName(e.target.value)}
            />
          </label>
          {!existingMode && (
            <div className={styles["sb-schedule-mode"]}>
              <span className={styles["sb-field-label"]}>Schedule timing</span>
              <div className={styles["sb-mode-btns"]}>
                <button
                  type="button"
                  className={`${styles["sb-mode-btn"]}${scheduleMode === "back_to_back" ? ` ${styles["is-active"]}` : ""}`}
                  onClick={() => setScheduleMode("back_to_back")}
                >
                  Back-to-back
                </button>
                <button
                  type="button"
                  className={`${styles["sb-mode-btn"]}${scheduleMode === "slot_grid" ? ` ${styles["is-active"]}` : ""}`}
                  onClick={() => setScheduleMode("slot_grid")}
                >
                  Snap to grid
                </button>
              </div>
              {scheduleMode === "slot_grid" && (
                <label className={styles["sb-slot-label"]}>
                  <span>start primary entries every</span>
                  <select
                    value={slotDurationMs}
                    onChange={(e) => setSlotDurationMs(Number(e.target.value) || DEFAULT_SLOT_DURATION_MS)}
                  >
                    <option value={30 * 60 * 1000}>30 minutes (:00 / :30)</option>
                    <option value={60 * 60 * 1000}>60 minutes (:00)</option>
                  </select>
                </label>
              )}
              <p className="muted">
                {scheduleMode === "slot_grid"
                  ? "Episodes keep their real duration; gaps are left for dead-air/filler packages in a later phase."
                  : "Episodes are packed continuously with no artificial wall-clock gaps."}
              </p>
            </div>
          )}
          {!existingMode && (
            <div className={styles["sb-schedule-mode"]}>
              <span className={styles["sb-field-label"]}>Playback</span>
              <div className={styles["sb-mode-btns"]}>
                <button
                  type="button"
                  className={`${styles["sb-mode-btn"]}${prefillMode === "on_demand" ? ` ${styles["is-active"]}` : ""}`}
                  onClick={() => { setPrefillMode("on_demand"); setAdaptiveBitrate(""); }}
                >
                  On-demand
                </button>
                <button
                  type="button"
                  className={`${styles["sb-mode-btn"]}${prefillMode === "eager" ? ` ${styles["is-active"]}` : ""}`}
                  onClick={() => setPrefillMode("eager")}
                >
                  Pre-encode
                </button>
              </div>
              <p className="muted">
                {prefillMode === "on_demand"
                  ? "Nothing is encoded until someone tunes in. The first viewer waits while the current program encodes; later viewers join the live edge. Idle channels cost nothing."
                  : "Every program is packaged ahead of time so tune-in is instant."}
              </p>
              {prefillMode === "eager" && channelMediaKind === "video" && (
                <label className={styles["sb-abr-select"]}>
                  <span>adaptive bitrate</span>
                  <select
                    value={adaptiveBitrate}
                    disabled={existingMode}
                    onChange={(e) => {
                      const val = e.target.value;
                      setAdaptiveBitrate(val);
                      if (val) {
                        setPackageProfile(defaultProfileRef.current || profiles[0] || "");
                      }
                    }}
                  >
                    <option value="">Off</option>
                    <option value="cpu">CPU (libx264)</option>
                    <option value="nvenc">NVIDIA (NVENC)</option>
                    <option value="hdr">HDR (HEVC copy + SDR fallback)</option>
                  </select>
                </label>
              )}
            </div>
          )}
          {!adaptiveBitrate && (
          <div className={styles["sb-profile-field"]}>
            <span className={styles["sb-field-label"]}>
              Select a package profile to get started
            </span>
            <div className={styles["sb-profile-btns"]}>
              {profiles.map((p) => (
                <button
                  key={p}
                  type="button"
                  className={`${styles["sb-profile-btn"]}${packageProfile === p ? ` ${styles["is-active"]}` : ""}`}
                  aria-pressed={packageProfile === p}
                  title={p}
                  disabled={existingMode}
                  onClick={() => setPackageProfile(p === packageProfile ? "" : p)}
                >
                  {profileDetails[p]?.label ?? p}
                </button>
              ))}
            </div>
          </div>
          )}
        </div>
      </section>

      {packageProfile && (
        <section className="admin-panel-section sb-list-section">
          {channelMediaKind === "video" && (
            <div className={styles["sb-content-btns"]}>
              <button
                type="button"
                className={styles["sb-content-btn"]}
                disabled={importBusy}
                title="Import a scraped list (JSON) and queue matching episodes"
                onClick={() => importInputRef.current?.click()}
              >
                {importBusy ? "Importing…" : "Import list"}
              </button>
              <input
                ref={importInputRef}
                type="file"
                accept="application/json,.json"
                hidden
                onChange={(e) => {
                  const file = e.target.files?.[0];
                  e.target.value = "";
                  if (!file) return;
                  file
                    .text()
                    .then((text) => importShowList(parseImportList(text)))
                    .catch((err) => setSearchStatus(err instanceof Error ? err.message : String(err)));
                }}
              />
            </div>
          )}
          <div className={styles["sb-content-btns"]}>
            {channelMediaKind === "video" ? (
              <>
                <button
                  type="button"
                  className={`${styles["sb-content-btn"]}${activeTab === "movies" ? ` ${styles["is-active"]}` : ""}`}
                  aria-pressed={activeTab === "movies"}
                  onClick={() => toggleTab("movies")}
                >
                  Movies
                </button>
                <button
                  type="button"
                  className={`${styles["sb-content-btn"]}${activeTab === "episodes" ? ` ${styles["is-active"]}` : ""}`}
                  aria-pressed={activeTab === "episodes"}
                  onClick={() => toggleTab("episodes")}
                >
                  Episodes
                </button>
              </>
            ) : (
              <button
                type="button"
                className={`${styles["sb-content-btn"]}${activeTab === "music" ? ` ${styles["is-active"]}` : ""}`}
                aria-pressed={activeTab === "music"}
                onClick={() => toggleTab("music")}
              >
                Music
              </button>
            )}
            {(existingMode || scheduleMode === "slot_grid") && (
              <button
                type="button"
                className={`${styles["sb-content-btn"]}${activeTab === "filler" ? ` ${styles["is-active"]}` : ""}`}
                aria-pressed={activeTab === "filler"}
                onClick={() => toggleTab("filler")}
              >
                Filler
              </button>
            )}
          </div>

          {activeTab === "episodes" && (
            <div className={styles["sb-picker-expanded"]}>
              <MediaPickerRail
                query={searchQuery}
                onQueryChange={setSearchQuery}
                queryPlaceholder="Search for a show or episode to add…"
                loading={searchBusy}
                loadingMessage="searching…"
                error={searchStatus}
                items={episodeItems}
                draggableItems
                onItemAction={(key) => {
                  const r = searchResults.find((x) => x.mediaId === key);
                  if (r && !candidateDisabled(r, profileDetails[packageProfile])) appendDraftEntry(candidateToInsertItem(r));
                }}
                toolsExtra={
                  <label className={styles["sb-ready-filter"]}>
                    <input
                      type="checkbox"
                      checked={readyOnly}
                      onChange={(e) => setReadyOnly(e.target.checked)}
                    />
                    Ready only
                  </label>
                }
                emptyMessage={
                  unaddedResults.length === 0
                    ? searchResults.length > 0
                      ? "all matching episodes are in the queue"
                      : searchQuery.trim()
                        ? "no candidates match"
                        : undefined
                    : undefined
                }
                footer={
                  !searchBusy && !searchStatus && unaddedResults.length > BUILDER_CANDIDATE_LIMIT ? (
                    <span className="muted">
                      Showing first {BUILDER_CANDIDATE_LIMIT} of {unaddedResults.length}; keep typing to narrow.
                    </span>
                  ) : undefined
                }
              />
            </div>
          )}

          {activeTab === "movies" && (
            <div className={styles["sb-picker-expanded"]}>
              <MediaPickerRail
                query={movieFilter}
                onQueryChange={setMovieFilter}
                queryPlaceholder="Filter movies…"
                loading={moviesLoading}
                loadingMessage="loading…"
                error={moviesError}
                items={movies
                  .filter((m) => !movieFilter.trim() || m.title.toLowerCase().includes(movieFilter.toLowerCase()))
                  .map((m) => ({
                    key: m.group,
                    title: m.title,
                    meta: m.itemCount > 1 ? `${m.itemCount} editions` : undefined,
                    durationMs: m.durationMs,
                    disabled: movieBusy === m.group,
                  }))}
                onItemAction={(key) => {
                  const m = movies.find((x) => x.group === key);
                  if (m) void queueMovie(m);
                }}
                emptyMessage={
                  moviesLoaded
                    ? movies.length === 0
                      ? "No movies found. Add a media source in 'Library' and scan to create schedule"
                      : "no movies match"
                    : undefined
                }
              />
            </div>
          )}

          {activeTab === "music" && (
            <div className={styles["sb-picker-expanded"]}>
              <SchedulePickerMusicGrid
                artists={artists}
                loading={artistsLoading}
                error={artistsError || undefined}
                filter={artistFilter}
                onFilterChange={setArtistFilter}
                selectedArtist={selectedArtist}
                onSelectArtist={setSelectedArtist}
                queueAlbum={queueAlbum}
                queueArtist={queueArtist}
                albumBusy={albumBusy}
                artistBusy={artistBusy}
                emptyMessage={
                  artistsLoaded
                    ? artists.length === 0
                      ? "No music found. Ingest a music library first."
                      : "no artists match"
                    : undefined
                }
              />
            </div>
          )}

          {activeTab === "filler" && (existingMode || scheduleMode === "slot_grid") && (
            <div className={styles["sb-picker-expanded"]}>
              <MediaPickerRail
                query={fillerQuery}
                onQueryChange={setFillerQuery}
                queryPlaceholder="Search filler assets…"
                loading={fillerLoading}
                loadingMessage="loading filler assets…"
                error={fillerError || undefined}
                draggableItems
                items={fillerCandidates
                  .filter((c) =>
                    !fillerQuery.trim() ||
                    c.label.toLowerCase().includes(fillerQuery.toLowerCase())
                  )
                  .map((c) => ({
                    key: c.mediaId,
                    title: c.label,
                    durationMs: c.packagedDurationMs ?? c.durationMs,
                    meta: c.packageReady ? undefined : c.packageStatus === "missing" ? "needs package" : c.packageStatus,
                    disabled: c.packageReady
                      ? false
                      : c.packageStatus === "pending" || c.packageStatus === "processing",
                    actionLabel: c.packageReady
                      ? undefined
                      : c.packageStatus === "missing"
                        ? "Encode"
                        : c.packageStatus === "failed"
                          ? "Retry"
                          : undefined,
                  }))}
                onItemAction={(mediaId) => void queueFillerPackage(mediaId)}
                itemActionBusy={fillerEncodeBusy.size > 0}
                emptyMessage={
                  fillerLoaded
                    ? fillerCandidates.length === 0
                      ? "No filler assets found. Add a local source with media kind 'filler' and scan it."
                      : "no filler matches"
                    : undefined
                }
                notice={
                  fillerLoaded && fillerCandidates.length > 0
                    ? "Drag a ready filler asset onto a gap in the timeline below."
                    : undefined
                }
                toolsExtra={
                  fillerLoaded && fillerCandidates.some((c) => !c.packageReady && c.packageStatus === "missing") ? (
                    <button
                      type="button"
                      disabled={fillerEncodeBusy.size > 0}
                      onClick={() => void queueAllMissingFiller()}
                    >
                      {fillerEncodeBusy.size > 0 ? "Queuing…" : "Queue all missing"}
                    </button>
                  ) : undefined
                }
              />
            </div>
          )}

          <div className="section-headline sb-queue-headline">
            <h3>
              {channelMediaKind === "music" ? "Tracks" : "Queue"}
              {scheduleDraft.length > 0 && (
                <span className="muted sb-list-meta">
                  {" "}({scheduleDraft.length} · {formatMs(totalMs)}{gapTotalMs > 0 ? ` · ${formatMs(gapTotalMs)} gaps` : ""})
                </span>
              )}
            </h3>
            {scheduleDraft.length > 0 && !preservesExistingWallClock && (
              <button type="button" className="danger" onClick={clearScheduleDraft}>
                Clear all
              </button>
            )}
            <button type="button" disabled={!canUndoScheduleDraft} onClick={undoScheduleDraftChange}>
              Undo
            </button>
          </div>

          {scheduleDraft.length === 0 ? (
            <>
              <p className="muted sb-empty">
                {channelMediaKind === "music"
                  ? "No tracks yet — open Music above and drag an artist or album onto the timeline."
                  : "No episodes yet — open Shows or Episodes above and drag media onto the timeline."}
              </p>
              <ScheduleTimeline
                windowStartMs={0}
                windowHours={1}
                nowMs={-1}
                entries={[]}
                unanchored
                onInsertMedia={insertMediaFromDrag}
                onInsertBatch={insertBatchFromDrag}
              />
            </>
          ) : (
            <>
              <ScheduleTimeline
                windowStartMs={timelineStartMs}
                windowHours={timelineWindowHours}
                nowMs={-1}
                entries={timelineEntries}
                unanchored={!preservesExistingWallClock}
                filledGaps={filledGaps}
                onReorder={preservesExistingWallClock ? undefined : moveDraftEntry}
                onInsertMedia={preservesExistingWallClock ? undefined : insertMediaFromDrag}
                onInsertBatch={preservesExistingWallClock ? undefined : insertBatchFromDrag}
              />
              <div className={styles["sb-list-preview"]}>
                <div className={styles["sb-list-preview-head"]}>
                  <h4>List preview</h4>
                  {isNewSlotGrid ? (
                    <span className="muted">Pick filler for each gap so episodes stay on the slot grid.</span>
                  ) : (
                    <span className="muted">Secondary row controls; the timeline above is the primary editor.</span>
                  )}
                  {isNewSlotGrid && [...gapAfter.values()].some((g) => g > 0) && (
                    <button
                      type="button"
                      onClick={fillRemainingGaps}
                      disabled={readyFiller.length === 0}
                      title="Assign the first long-enough filler to every gap that isn't filled yet."
                    >
                      Fill remaining gaps
                    </button>
                  )}
                </div>
                <ul className={styles["sb-entry-list"]}>
                  {scheduleDraft.map((e, index) => {
                    const gapMs = gapAfter.get(e.draftId) ?? 0;
                    const assigned = gapFillerByPrimaryId.get(e.draftId) ?? "";
                    const eligible = readyFiller.filter((f) => f.packagedDurationMs >= gapMs);
                    const assignedValid = assigned !== "" && eligible.some((f) => f.mediaId === assigned);
                    return (
                      <Fragment key={e.draftId}>
                        <li className={styles["sb-entry"]}>
                          <div className={styles["sb-entry-main"]}>
                            <span className={styles["sb-entry-title"]}>{e.title || e.mediaId}</span>
                            {e.path && (
                              <span className={`${styles["sb-entry-sub"]} muted`} title={e.path}>
                                {sourceTail(e.path)}
                              </span>
                            )}
                          </div>
                          <span className="sb-entry-duration muted">{formatMs(e.durationMs)}</span>
                          <div className={styles["sb-entry-move"]}>
                            <button type="button" disabled={preservesExistingWallClock || index === 0} onClick={() => moveDraftEntry(index, index - 1)} aria-label="Move up">↑</button>
                            <button type="button" disabled={preservesExistingWallClock || index === scheduleDraft.length - 1} onClick={() => moveDraftEntry(index, index + 1)} aria-label="Move down">↓</button>
                          </div>
                          <button type="button" className={styles["sb-entry-remove"]} disabled={preservesExistingWallClock} aria-label="Remove" onClick={() => removeDraftEntry(index)}>✕</button>
                        </li>
                        {isNewSlotGrid && gapMs > 0 && (
                          <li className={styles["sb-gap-row"]}>
                            <span className="muted">↳ gap {formatMs(gapMs)} · filler after:</span>
                            <select
                              value={assignedValid ? assigned : ""}
                              onChange={(ev) => assignGapFiller(e.draftId, ev.target.value)}
                            >
                              <option value="">— none —</option>
                              {eligible.map((f) => (
                                <option key={f.mediaId} value={f.mediaId}>{f.title}</option>
                              ))}
                            </select>
                            {!assignedValid && (
                              <span className="error">
                                {eligible.length === 0 ? "no filler long enough" : "unfilled gap"}
                              </span>
                            )}
                          </li>
                        )}
                      </Fragment>
                    );
                  })}
                </ul>
              </div>
            </>
          )}

          <div className={styles["sb-import-row"]}>
            {preservesExistingWallClock ? (
              <>
                <button
                  type="button"
                  className="primary"
                  disabled={mutationBusy}
                  onClick={() => void recomposeSlotGrid()}
                  title="Clear the schedule after the current program and rebuild it gap-free: primaries on slot boundaries, filler auto-tiled from this channel's attached filler assets."
                >
                  Recompose gap-free
                </button>
                <span className="muted">Rebuilds the future gap-free from this channel's media and attached filler.</span>
              </>
            ) : (
              <button
                type="button"
                className="primary"
                disabled={saveBusy || scheduleDraft.length === 0 || !displayName.trim() || slotGapsRemain}
                onClick={() =>
                  void (existingMode
                    ? saveScheduleEdit()
                    : importDraftChannel(
                        slotGridFill
                          ? { entries: slotGridFill.entries, fillerMediaIds: slotGridFill.fillerMediaIds }
                          : undefined,
                      ))
                }
                title={
                  existingMode
                    ? "Save this draft back to the existing channel."
                    : slotGapsRemain
                      ? "Fill every slot gap with filler before creating the channel."
                      : "Create channel and queue any unpackaged media."
                }
              >
                {importButtonLabel}
              </button>
            )}
            {!existingMode && scheduleMode === "slot_grid" && scheduleDraft.length > 0 && slotGridFill && (
              <span className={slotGapsRemain ? "error" : "muted"}>
                {slotGapsRemain
                  ? `${slotGridFill.unfilledGapCount} gap${slotGridFill.unfilledGapCount === 1 ? "" : "s"} need filler (${formatMs(slotGridFill.unfilledGapMs)}) — pick filler per episode below`
                  : slotGridFill.filledGapMs > 0
                    ? `gaps filled · ${formatMs(slotGridFill.filledGapMs)} filler`
                    : "no gaps"}
              </span>
            )}
            {statusMessage && (
              <span className={scheduleError ? "error" : "muted"}>{statusMessage}</span>
            )}
          </div>
        </section>
      )}
    </div>
  );
}

function profileChipLabel(name: string, details: Record<string, PackageProfile>) {
  if (!name) return "selected profile";
  return details[name]?.label || name;
}

// sourceTail returns the disambiguating part of a media filename for the list
// preview subline: the source/quality/release tokens after the release year.
// Two files of the same movie share a long "Title.YYYY" prefix that is already
// shown on the title line and would otherwise eat the column with end-ellipsis,
// so we drop it. Falls back to the bare filename when no year token is present.
function sourceTail(path: string): string {
  const base = path.split("/").pop() ?? path;
  const yearRe = /(?:19|20)\d{2}/g;
  let last: RegExpExecArray | null = null;
  for (let m = yearRe.exec(base); m; m = yearRe.exec(base)) last = m;
  if (!last) return base;
  const tail = base.slice(last.index + last[0].length).replace(/^[.\s_-]+/, "");
  return tail || base;
}

// Converts the media shape from getScheduleBuilderGroup into a ScheduleInsertItem.
// getScheduleBuilderGroup returns a different type than MediaPackageCandidate (no packageStatus).
function candidateToInsertItemFromMedia(m: {
  mediaId: string;
  title?: string;
  path: string;
  collectionName?: string;
  durationMs: number;
  packagedDurationMs?: number;
}, forceReady = false): ScheduleInsertItem {
  return {
    mediaId: m.mediaId,
    title: m.title,
    path: m.path,
    collectionName: m.collectionName,
    durationMs: m.packagedDurationMs ?? m.durationMs,
    packagedDurationMs: m.packagedDurationMs,
    packageReady: forceReady || m.packagedDurationMs != null,
    channelMember: false,
  };
}
