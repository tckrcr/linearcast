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
  getAllMediaInventory,
  getMediaAlbums,
  getMediaByGroup,
  getMediaInventory,
  getMediaMovies,
  getMediaPackageProfileList,
  getMediaShows,
  getScheduleBuilderFillerCandidates,
  requestMediaPackages,
} from "../api";
import type { MediaInventoryItem, MediaMovie, MediaShow, MusicArtist } from "../api/media";
import { formatMs } from "../format";
import { useScheduleEditor } from "../hooks/useScheduleEditor";
import { useHasMediaSource } from "../hooks/useHasMediaSource";
import type { ChannelNow, FillerAssetCandidateItem, PackageProfile, ScheduleInsertItem } from "../types";
import { MediaPickerRail } from "./MediaPickerRail";
import { SchedulePickerMusicGrid } from "./SchedulePickerMusicGrid";
import { SchedulePickerShows } from "./SchedulePickerShows";
import { ScheduleTimeline } from "./ScheduleTimeline";
import styles from "./ScheduleBuilderPanel.module.css";

type PickerTab = "shows" | "movies" | "music" | "filler";
type ScheduleBatchDragPayload =
  | { kind: "group"; group: string }
  | { kind: "album"; group: string }
  | { kind: "artist"; artistName?: string };

function packageStatusLabel(item: MediaInventoryItem, profile: string, profileDetails: Record<string, PackageProfile>): string {
  const status = item.profilePackageStatus;
  if (!status || status === "ready") return "";
  if (status === "missing") return "needs package";
  if (status === "failed") return `failed at ${profileChipLabel(profile, profileDetails)}`;
  return `${status} at ${profileChipLabel(profile, profileDetails)}`;
}

function sourceBitrateLabel(item: MediaInventoryItem): string {
  // A ready package already has its encoded size; the source bitrate only
  // says something about rows still waiting to be encoded.
  if (item.profilePackageStatus === "ready") return "";
  if (!item.videoBitrateBps || item.videoBitrateBps <= 0) return "";
  return `${(item.videoBitrateBps / 1_000_000).toFixed(1)} Mbps source`;
}

function candidateToInsertItem(r: MediaInventoryItem, forceReady = false): ScheduleInsertItem {
  return {
    mediaId: r.mediaId,
    title: r.title || undefined,
    path: r.path,
    collectionName: r.collection || undefined,
    durationMs: r.packagedDurationMs ?? r.durationMs,
    packagedDurationMs: r.packagedDurationMs,
    packageReady: forceReady || r.profilePackageStatus === "ready",
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
  active = true,
  onChannelImported,
  onOpenMediaSources,
}: {
  existingChannel?: ChannelNow;
  active?: boolean;
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
  const [adaptiveBitrate, setAdaptiveBitrate] = useState<"" | "cpu" | "hdr">("");
  const defaultProfileRef = useRef("");
  const [profiles, setProfiles] = useState<string[]>([]);
  const [profileDetails, setProfileDetails] = useState<Record<string, PackageProfile>>({});
  const [profilesLoading, setProfilesLoading] = useState(true);
  const [profilesError, setProfilesError] = useState("");

  // Picker tab state — null means no content panel is open
  const [activeTab, setActiveTab] = useState<PickerTab | null>(null);

  // Shows browser and cross-show episode search state
  const [searchQuery, setSearchQuery] = useState("");
  const [searchResults, setSearchResults] = useState<MediaInventoryItem[]>([]);
  const [searchBusy, setSearchBusy] = useState(false);
  const [searchStatus, setSearchStatus] = useState("");
  const [readyOnly, setReadyOnly] = useState(false);
  const [importBusy, setImportBusy] = useState(false);
  const nameInputRef = useRef<HTMLInputElement>(null);
  const importInputRef = useRef<HTMLInputElement>(null);
  const searchTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const [shows, setShows] = useState<MediaShow[]>([]);
  const [showsLoaded, setShowsLoaded] = useState(false);
  const [showsLoading, setShowsLoading] = useState(false);
  const [showsError, setShowsError] = useState("");
  const [selectedShow, setSelectedShow] = useState<MediaShow | null>(null);
  const [showEpisodes, setShowEpisodes] = useState<MediaInventoryItem[]>([]);
  const [showEpisodesLoading, setShowEpisodesLoading] = useState(false);
  const [showEpisodesError, setShowEpisodesError] = useState("");

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
    setProfilesLoading(true);
    setProfilesError("");
    getMediaPackageProfileList()
      .then((next) => {
        const details = Object.fromEntries(next.profileDetails.map((item) => [item.name, item]));
        const selectable = next.profiles;
        defaultProfileRef.current = next.defaultProfile;
        setProfiles(selectable);
        setProfileDetails(details);
        if (!existingMode && !selectable.includes(next.defaultProfile)) {
          setProfilesError("The configured default package profile is unavailable.");
        }
        setPackageProfile((current) => {
          if (existingMode) return existingChannel?.packageProfile ?? current;
          if (selectable.includes(current)) return current;
          return selectable.includes(next.defaultProfile) ? next.defaultProfile : "";
        });
      })
      .catch((err) => {
        setProfilesError(err instanceof Error ? err.message : String(err));
      })
      .finally(() => setProfilesLoading(false));
  }, [existingMode, existingChannel?.packageProfile]);

  useEffect(() => {
    if (prefillMode === "on_demand") setReadyOnly(false);
  }, [prefillMode]);

  useEffect(() => {
    if (!active || existingMode || sourceGate.loading || !sourceGate.hasMediaSource) return;
    if (!displayName.trim()) nameInputRef.current?.focus();
  }, [active, existingMode, sourceGate.loading, sourceGate.hasMediaSource, displayName]);

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
    getMediaMovies()
      .then((m) => { setMovies(m); setMoviesLoaded(true); })
      .catch((err) => setMoviesError(err instanceof Error ? err.message : String(err)))
      .finally(() => setMoviesLoading(false));
  }, [activeTab, moviesLoaded, moviesLoading]);

  // The Shows entry opens onto a populated collection browser. Episode search
  // remains profile-aware so eager workflows can still explain readiness.
  useEffect(() => {
    if (activeTab !== "shows" || showsLoaded || showsLoading) return;
    setShowsLoading(true);
    setShowsError("");
    getMediaShows()
      .then((next) => {
        setShows(next);
        setShowsLoaded(true);
      })
      .catch((err) => setShowsError(err instanceof Error ? err.message : String(err)))
      .finally(() => setShowsLoading(false));
  }, [activeTab, showsLoaded, showsLoading]);

  useEffect(() => {
    if (activeTab !== "shows" || !selectedShow || !packageProfile) return;
    let cancelled = false;
    setShowEpisodesLoading(true);
    setShowEpisodesError("");
    getAllMediaInventory({
      collection: selectedShow.name,
      profile: packageProfile,
      kind: "shows",
      codecStatus: "passed",
    })
      .then((next) => {
        if (!cancelled) setShowEpisodes(next);
      })
      .catch((err) => {
        if (!cancelled) {
          setShowEpisodes([]);
          setShowEpisodesError(err instanceof Error ? err.message : String(err));
        }
      })
      .finally(() => {
        if (!cancelled) setShowEpisodesLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [activeTab, selectedShow, packageProfile]);

  // Lazy-load music
  useEffect(() => {
    if (activeTab !== "music" || artistsLoaded || artistsLoading) return;
    setArtistsLoading(true);
    setArtistsError("");
    getMediaAlbums()
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

  // Debounced individual-episode search. An empty query is served entirely by
  // the already-loaded show collection browser.
  useEffect(() => {
    if (activeTab !== "shows" || !packageProfile) return;
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
      getMediaInventory({
        q,
        profile: packageProfile,
        profilePackageStatus: readyOnly ? "ready" : undefined,
        kind: "shows",
        codecStatus: "passed",
      })
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
      const media = await getMediaByGroup(group);
      const added = appendDraftEntries(media.map((m) => candidateToInsertItemFromMedia(m)), index);
      if (!displayName.trim()) setDisplayName(group);
      if (added === 0) setSearchStatus(`all episodes from "${group}" already in queue`);
    } catch (err) {
      setSearchStatus(err instanceof Error ? err.message : String(err));
    } finally {
      setGroupBusy(null);
    }
  }

  function queueEpisode(episode: MediaInventoryItem, index?: number) {
    appendDraftEntry(candidateToInsertItem(episode), index);
    if (!displayName.trim() && episode.collection.trim()) setDisplayName(episode.collection);
  }

  function queueSeason(show: MediaShow, seasonNumber: number | null, index?: number) {
    const seasonEpisodes = showEpisodes.filter(
      (episode) => (episode.seasonNumber ?? null) === seasonNumber,
    );
    const added = appendDraftEntries(seasonEpisodes.map((episode) => candidateToInsertItem(episode)), index);
    if (!displayName.trim()) setDisplayName(show.name);
    if (added === 0) {
      const label = seasonNumber == null ? "other episodes" : `season ${seasonNumber}`;
      setSearchStatus(`all ${label} from "${show.name}" already in queue`);
    }
  }

  async function queueMovie(movie: MediaMovie, index?: number) {
    if (movieBusy) return;
    setMovieBusy(movie.group);
    try {
      const media = await getMediaByGroup(movie.group);
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
      const media = await getMediaByGroup(group);
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
      const batches = await Promise.all(artist.albums.map((al) => getMediaByGroup(al.group)));
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
      const all = await getAllMediaInventory({ profile: packageProfile, kind: "programs", codecStatus: "passed" });
      const haystacks = all.map((c) => normalizeForMatch(`${c.title} ${c.path}`));
      const picked: MediaInventoryItem[] = [];
      const unmatched: string[] = [];
      for (const it of items) {
        const ep = normalizeForMatch(it.episode);
        const show = it.show ? normalizeForMatch(it.show) : "";
        const i = ep ? haystacks.findIndex((h) => h.includes(ep) && (!show || h.includes(show))) : -1;
        if (i >= 0) picked.push(all[i]);
        else unmatched.push(it.show ? `${it.show} — ${it.episode}` : it.episode);
      }
      const added = appendDraftEntries(picked.map((m) => candidateToInsertItem(m)));
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
    if (tab === "shows") setSelectedShow(null);
    setActiveTab(activeTab === tab ? null : tab);
  }

  function insertMediaFromDrag(key: string, index: number) {
    const r = searchResults.find((x) => x.mediaId === key);
    if (r) queueEpisode(r, index);
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
      : prefillMode === "on_demand" || allKnownReady
        ? "Create channel"
        : "Create channel and queue packages";
  const statusMessage = scheduleError || scheduleNotice;
  const creationStep = !displayName.trim() ? 1 : scheduleDraft.length === 0 ? 2 : 3;

  // ---------------------------------------------------------------------------
  // Source gate
  // ---------------------------------------------------------------------------

  if (!existingMode && sourceGate.loading) {
    return (
      <div className="admin-panel">
        <section className="admin-panel-section">
          <h2>Create a channel</h2>
          <p className="muted">checking media sources...</p>
        </section>
      </div>
    );
  }

  if (!existingMode && !sourceGate.hasMediaSource) {
    return (
      <div className="admin-panel">
        <section className="admin-panel-section">
          <h2>Create a channel</h2>
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
  const normalizedEpisodeQuery = normalizeForMatch(searchQuery);
  const matchingShowNames = new Set(
    shows
      .filter((show) => normalizeForMatch(show.name).includes(normalizedEpisodeQuery))
      .map((show) => normalizeForMatch(show.name)),
  );
  const visibleSearchResults = unaddedResults
    .filter((result) => {
      const collection = normalizeForMatch(result.collection);
      let directQuery = normalizedEpisodeQuery;
      if (collection && directQuery.startsWith(`${collection} `)) {
        directQuery = directQuery.slice(collection.length).trim();
      } else if (matchingShowNames.has(collection)) {
        return false;
      }
      if (!directQuery) return false;
      const filename = result.path.split("/").pop() ?? result.path;
      return normalizeForMatch(`${result.episodeCode ?? ""} ${result.title} ${filename}`).includes(directQuery);
    })
    .slice(0, BUILDER_CANDIDATE_LIMIT);
  const episodeItems = visibleSearchResults.map((r) => {
    const meta = [
      r.collection,
      prefillMode === "eager" ? sourceBitrateLabel(r) : "",
      prefillMode === "eager" ? packageStatusLabel(r, packageProfile, profileDetails) : "",
    ].filter(Boolean).join(" · ");
    return {
      key: r.mediaId,
      title: r.title || r.path.split("/").pop() || r.path,
      meta: meta || undefined,
      durationMs: r.packagedDurationMs ?? r.durationMs,
    };
  });

  // ---------------------------------------------------------------------------
  // Render
  // ---------------------------------------------------------------------------

  return (
    <div className="admin-panel sb-panel">
      <section className={`admin-panel-section ${styles["sb-intro-section"]}`}>
        <div className="section-headline">
          <div className="section-headline-main">
            <h2>{existingMode ? `Edit schedule: ${displayName}` : "Create a channel"}</h2>
            <p className="section-purpose">
              {existingMode
                ? "Adjust the programs in this channel without changing its playback or timing policy."
                : "Name the channel, choose what it plays, and review the schedule before creating it."}
            </p>
          </div>
        </div>
        {!existingMode && (
          <ol className={styles["sb-progress"]} aria-label="Channel creation progress">
            {[
              { step: 1, label: "Name" },
              { step: 2, label: "Content" },
              { step: 3, label: "Review" },
            ].map((item) => {
              const complete =
                item.step === 1
                  ? displayName.trim() !== ""
                  : item.step === 2
                    ? scheduleDraft.length > 0
                    : false;
              return (
                <li
                  key={item.step}
                  className={
                    complete
                      ? styles["is-complete"]
                      : item.step === creationStep
                        ? styles["is-current"]
                        : undefined
                  }
                  aria-current={item.step === creationStep ? "step" : undefined}
                >
                  <span>{complete ? "✓" : item.step}</span>
                  {item.label}
                </li>
              );
            })}
          </ol>
        )}
        <div className={styles["sb-config"]}>
          <label className={styles["sb-name-label"]}>
            <span>{existingMode ? "Channel name" : "1 · Channel name"}</span>
            <input
              ref={nameInputRef}
              value={displayName}
              aria-label="Channel name"
              placeholder="e.g. Sunday comedies"
              disabled={existingMode}
              onChange={(e) => setDisplayName(e.target.value)}
            />
          </label>
          {profilesLoading && !existingMode && (
            <p className={`muted ${styles["sb-profile-status"]}`}>Loading channel defaults…</p>
          )}
          {profilesError && (
            <p className={`error ${styles["sb-profile-status"]}`}>{profilesError}</p>
          )}
          {packageProfile && (
            <details className={styles["sb-advanced"]} open={existingMode || undefined}>
              <summary>
                <span>{existingMode ? "Channel policy" : "Advanced options"}</span>
                <span className={styles["sb-policy-summary"]}>
                  {prefillMode === "on_demand" ? "On-demand" : "Pre-encode"}
                  {" · "}
                  {scheduleMode === "slot_grid" ? "Grid aligned" : "Back-to-back"}
                  {" · "}
                  {profileChipLabel(packageProfile, profileDetails)}
                </span>
              </summary>
              <div className={styles["sb-advanced-body"]}>
                <div className={styles["sb-option-group"]}>
                  <span className={styles["sb-field-label"]}>Playback preparation</span>
                  <p className="muted">
                    On-demand starts quickly without preparing every program. Pre-encode trades creation follow-up work for faster first tune.
                  </p>
                  <div className={styles["sb-mode-btns"]}>
                    <button
                      type="button"
                      className={`${styles["sb-mode-btn"]}${prefillMode === "on_demand" ? ` ${styles["is-active"]}` : ""}`}
                      disabled={existingMode}
                      onClick={() => { setPrefillMode("on_demand"); setAdaptiveBitrate(""); }}
                    >
                      On-demand
                    </button>
                    <button
                      type="button"
                      className={`${styles["sb-mode-btn"]}${prefillMode === "eager" ? ` ${styles["is-active"]}` : ""}`}
                      disabled={existingMode}
                      onClick={() => setPrefillMode("eager")}
                    >
                      Pre-encode
                    </button>
                  </div>
                  {prefillMode === "eager" && channelMediaKind === "video" && !existingMode && (
                    <label className={styles["sb-abr-select"]}>
                      <span>Adaptive bitrate</span>
                      <select
                        value={adaptiveBitrate}
                        onChange={(e) => {
                          const val = e.target.value as "" | "cpu" | "hdr";
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
                {!adaptiveBitrate && (
                  <div className={styles["sb-option-group"]}>
                    <span className={styles["sb-field-label"]}>Package profile</span>
                    <p className="muted">The configured default is selected automatically. Choose another only when this channel needs it.</p>
                    <div className={styles["sb-profile-btns"]}>
                      {profiles.map((p) => (
                        <button
                          key={p}
                          type="button"
                          className={`${styles["sb-profile-btn"]}${packageProfile === p ? ` ${styles["is-active"]}` : ""}`}
                          aria-pressed={packageProfile === p}
                          title={p}
                          disabled={existingMode}
                          onClick={() => setPackageProfile(p)}
                        >
                          {profileDetails[p]?.label ?? p}
                        </button>
                      ))}
                    </div>
                  </div>
                )}
              </div>
            </details>
          )}
        </div>
      </section>

      {packageProfile && (
        <section className={`admin-panel-section ${styles["sb-list-section"]} ${styles["sb-workspace"]}`}>
          <div className="section-headline">
            <div className="section-headline-main">
              <h3>{existingMode ? "Add content" : "2 · Choose content"}</h3>
              <p className="section-purpose">
                Pick individual programs or add a whole collection. You can reorder everything in the draft below.
              </p>
            </div>
            {channelMediaKind === "video" && (
              <div className="section-headline-actions">
                <button
                  type="button"
                  disabled={importBusy}
                  title="Import a scraped list (JSON) and queue matching episodes"
                  onClick={() => importInputRef.current?.click()}
                >
                  {importBusy ? "Importing…" : "Import JSON list"}
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
          </div>
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
                  className={`${styles["sb-content-btn"]}${activeTab === "shows" ? ` ${styles["is-active"]}` : ""}`}
                  aria-pressed={activeTab === "shows"}
                  onClick={() => toggleTab("shows")}
                >
                  Shows
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

          {activeTab === "shows" && (
            <div className={styles["sb-picker-expanded"]}>
              <SchedulePickerShows
                shows={shows}
                showsLoading={showsLoading}
                showsError={showsError}
                query={searchQuery}
                onQueryChange={setSearchQuery}
                searchLoading={searchBusy}
                searchError={searchStatus}
                episodeItems={episodeItems}
                onAddEpisode={(key) => {
                  const r = searchResults.find((x) => x.mediaId === key);
                  if (r) queueEpisode(r);
                }}
                toolsExtra={prefillMode === "eager" ? (
                  <label className={styles["sb-ready-filter"]}>
                    <input
                      type="checkbox"
                      checked={readyOnly}
                      onChange={(e) => setReadyOnly(e.target.checked)}
                    />
                    Ready only
                  </label>
                ) : undefined}
                selectedShow={selectedShow}
                onSelectShow={setSelectedShow}
                showEpisodes={showEpisodes}
                showEpisodesLoading={showEpisodesLoading}
                showEpisodesError={showEpisodesError}
                selectedEpisodeKeys={selectedMediaKeys}
                onQueueShow={(show) => void queueGroup(show.name)}
                onQueueSeason={queueSeason}
                onQueueEpisode={queueEpisode}
                groupBusy={groupBusy}
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

          <div className={styles["sb-draft-card"]}>
            <div className={`section-headline ${styles["sb-draft-headline"]}`}>
              <div className="section-headline-main">
                <h3>Draft schedule</h3>
                <p className="section-purpose">
                  {scheduleDraft.length > 0
                    ? `${scheduleDraft.length} ${channelMediaKind === "music" ? "track" : "program"}${scheduleDraft.length === 1 ? "" : "s"} · ${formatMs(totalMs)}${gapTotalMs > 0 ? ` · ${formatMs(gapTotalMs)} gaps` : ""}`
                    : "Added content will appear here in its initial play order."}
                </p>
              </div>
              <div className="section-headline-actions">
                {scheduleDraft.length > 0 && !preservesExistingWallClock && (
                  <button type="button" className="danger" onClick={clearScheduleDraft}>
                    Clear all
                  </button>
                )}
                <button type="button" disabled={!canUndoScheduleDraft} onClick={undoScheduleDraftChange}>
                  Undo
                </button>
              </div>
            </div>

            {!existingMode && scheduleDraft.length > 0 && (
              <div className={styles["sb-grid-option"]}>
                <label>
                  <input
                    type="checkbox"
                    checked={scheduleMode === "slot_grid"}
                    onChange={(e) => {
                      setScheduleMode(e.target.checked ? "slot_grid" : "back_to_back");
                      if (!e.target.checked) setGapFillerByPrimaryId(new Map());
                    }}
                  />
                  <span>
                    <strong>Align programs to a time grid</strong>
                    <small>Start programs on predictable clock boundaries and fill the resulting gaps.</small>
                  </span>
                </label>
                {scheduleMode === "slot_grid" && (
                  <label className={styles["sb-slot-label"]}>
                    <span>Start every</span>
                    <select
                      value={slotDurationMs}
                      onChange={(e) => setSlotDurationMs(Number(e.target.value) || DEFAULT_SLOT_DURATION_MS)}
                    >
                      <option value={30 * 60 * 1000}>30 minutes (:00 / :30)</option>
                      <option value={60 * 60 * 1000}>60 minutes (:00)</option>
                    </select>
                  </label>
                )}
              </div>
            )}

          {scheduleDraft.length === 0 ? (
            <>
              <p className={`muted ${styles["sb-empty"]}`}>
                {channelMediaKind === "music"
                  ? "Choose Music above, then add an artist or album."
                  : "Choose Movies or Shows above, then add something you want this channel to play."}
              </p>
              <div className={styles["sb-empty-timeline"]}>
                <ScheduleTimeline
                  windowStartMs={0}
                  windowHours={1}
                  nowMs={-1}
                  entries={[]}
                  unanchored
                  onInsertMedia={insertMediaFromDrag}
                  onInsertBatch={insertBatchFromDrag}
                />
              </div>
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
                  <h4>{channelMediaKind === "music" ? "Track order" : "Program order"}</h4>
                  {isNewSlotGrid ? (
                    <span className="muted">Pick filler for each gap so episodes stay on the slot grid.</span>
                  ) : !existingMode ? (
                    <span className="muted">This order repeats to fill the initial channel schedule.</span>
                  ) : (
                    <span className="muted">Use the row controls to fine-tune the play order.</span>
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

          <div
            className={styles["sb-commit"]}
            role={!existingMode && scheduleDraft.length > 0 ? "region" : undefined}
            aria-label={!existingMode && scheduleDraft.length > 0 ? "Channel summary" : undefined}
          >
            {!existingMode && scheduleDraft.length > 0 && (
              <div className={styles["sb-summary"]}>
                <div>
                  <h4>Channel summary</h4>
                  <p className="muted">
                    {prefillMode === "on_demand"
                      ? "Creating commits the channel immediately; playback prepares on first tune."
                      : allKnownReady
                        ? "Creating commits the channel immediately with its selected media already packaged."
                        : "Creating commits the channel immediately, then queues missing packages."}
                  </p>
                </div>
                <dl>
                  <div>
                    <dt>Name</dt>
                    <dd>{displayName.trim() || "Name required"}</dd>
                  </div>
                  <div>
                    <dt>Programming</dt>
                    <dd>{scheduleDraft.length} {channelMediaKind === "music" ? "track" : "program"}{scheduleDraft.length === 1 ? "" : "s"} · {formatMs(totalMs)}</dd>
                  </div>
                  <div>
                    <dt>Playback</dt>
                    <dd>{prefillMode === "on_demand" ? "On-demand" : "Pre-encode"}</dd>
                  </div>
                  <div>
                    <dt>Schedule</dt>
                    <dd>{scheduleMode === "slot_grid" ? `Grid aligned · ${formatMs(validSlotDurationMs)}` : "Back-to-back"}</dd>
                  </div>
                  <div>
                    <dt>Profile</dt>
                    <dd>{profileChipLabel(packageProfile, profileDetails)}</dd>
                  </div>
                </dl>
              </div>
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
                      : prefillMode === "on_demand"
                        ? "Create the channel now. Playback is prepared when someone first tunes in."
                        : "Create the channel and queue any unpackaged media."
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
          </div>
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

// Converts the media shape from getMediaByGroup into a ScheduleInsertItem.
// getMediaByGroup returns a different type than MediaPackageCandidate (no packageStatus).
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
