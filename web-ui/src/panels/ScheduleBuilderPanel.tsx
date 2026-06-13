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
  getScheduleBuilderAlbums,
  getScheduleBuilderCandidates,
  getScheduleBuilderFillerCandidates,
  getScheduleBuilderGroup,
  getScheduleBuilderProfileList,
  getScheduleBuilderShows,
} from "../api";
import type { MediaShow, MusicArtist } from "../api/media";
import { formatMs } from "../format";
import { useScheduleEditor } from "../hooks/useScheduleEditor";
import { useHasMediaSource } from "../hooks/useHasMediaSource";
import type { ChannelNow, FillerAssetCandidateItem, MediaPackageCandidate, PackageProfile, ScheduleInsertItem } from "../types";
import { MediaPickerRail } from "./MediaPickerRail";
import { SchedulePickerMusicGrid } from "./SchedulePickerMusicGrid";
import { SchedulePickerShowsGrid } from "./SchedulePickerShowsGrid";
import { ScheduleTimeline } from "./ScheduleTimeline";
import styles from "./ScheduleBuilderPanel.module.css";

type PickerTab = "episodes" | "shows" | "music" | "filler";
type ScheduleBatchDragPayload =
  | { kind: "group"; group: string }
  | { kind: "show"; showName: string }
  | { kind: "album"; group: string }
  | { kind: "artist"; artistName?: string };

function packageStatusLabel(candidate: MediaPackageCandidate, profileDetails: Record<string, PackageProfile>): string {
  if (candidate.packageStatus === "ready") return "";
  if (candidate.packageStatus === "missing") return "needs package";
  const profile = candidate.packageProfile || "";
  if (candidate.packageStatus === "failed") return `failed at ${profileChipLabel(profile, profileDetails)}`;
  return `${candidate.packageStatus} at ${profileChipLabel(profile, profileDetails)}`;
}

function candidateToInsertItem(r: MediaPackageCandidate, forceReady = false): ScheduleInsertItem {
  return {
    mediaId: r.mediaId,
    title: r.title,
    path: r.path,
    schedulingGroup: r.schedulingGroup,
    durationMs: r.packagedDurationMs ?? r.durationMs,
    packagedDurationMs: r.packagedDurationMs,
    packageReady: forceReady || r.packageStatus === "ready",
    channelMember: false,
  };
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
  const [playbackMode, setPlaybackMode] = useState<"packaged" | "plex_relay">(
    existingChannel?.playbackMode === "plex_relay" ? "plex_relay" : "packaged",
  );
  // On-demand defers packaging until a viewer tunes in. Create-time only; an
  // existing channel's prefill mode is shown read-only via existingChannel.
  const [onDemand, setOnDemand] = useState(existingChannel?.prefillMode === "on_demand");
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
  const searchTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  // Shows-tab state
  const [shows, setShows] = useState<MediaShow[]>([]);
  const [showsLoaded, setShowsLoaded] = useState(false);
  const [showsLoading, setShowsLoading] = useState(false);
  const [showsError, setShowsError] = useState("");
  const [groupFilter, setGroupFilter] = useState("");
  const [groupBusy, setGroupBusy] = useState<string | null>(null);
  const [showBusy, setShowBusy] = useState<string | null>(null);
  const [selectedShow, setSelectedShow] = useState<MediaShow | null>(null);

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
  // New-channel slot-grid only: which filler clip fills the gap after each
  // episode, keyed by the episode's draftId so the choice survives reorders.
  // The schedule must be gap-free (every gap assigned) before it can be saved.
  const [gapFillerByPrimaryId, setGapFillerByPrimaryId] = useState<Map<string, string>>(new Map());

  const channelMediaKind: "video" | "music" =
    playbackMode === "plex_relay" ? "video" : profileDetails[packageProfile]?.mediaKind === "music" ? "music" : "video";
  const isPlexRelay = !existingMode && playbackMode === "plex_relay";

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
          playbackMode,
          scheduleMode,
          slotDurationMs,
          prefillMode: playbackMode === "plex_relay" ? "eager" : onDemand ? "on_demand" : "eager",
          adaptiveBitrate: (playbackMode === "packaged" && !onDemand && channelMediaKind === "video" && adaptiveBitrate) || undefined,
          onImported: onChannelImported,
        },
  );

  useEffect(() => {
    if (!existingChannel) return;
    setDisplayName(existingChannel.displayName);
    setPackageProfile(existingChannel.packageProfile);
    setPlaybackMode(existingChannel.playbackMode === "plex_relay" ? "plex_relay" : "packaged");
    setScheduleMode(existingChannel.scheduleMode === "slot_grid" ? "slot_grid" : "back_to_back");
    setSlotDurationMs(existingChannel.slotDurationMs ?? DEFAULT_SLOT_DURATION_MS);
  }, [existingChannel?.id, existingChannel?.displayName, existingChannel?.packageProfile, existingChannel?.playbackMode, existingChannel?.scheduleMode, existingChannel?.slotDurationMs]);

  // Profiles
  useEffect(() => {
    getScheduleBuilderProfileList()
      .then((next) => {
        const details = Object.fromEntries(next.profileDetails.map((item) => [item.name, item]));
        const selectable = next.profiles.filter((profile) => !isABRProfile(details[profile]));
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

  // Lazy-load shows
  useEffect(() => {
    if (activeTab !== "shows" || showsLoaded || showsLoading) return;
    setShowsLoading(true);
    setShowsError("");
    getScheduleBuilderShows()
      .then((s) => { setShows(s); setShowsLoaded(true); })
      .catch((err) => setShowsError(err instanceof Error ? err.message : String(err)))
      .finally(() => setShowsLoading(false));
  }, [activeTab, showsLoaded, showsLoading]);

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

  // Debounced episode search
  useEffect(() => {
    if (activeTab !== "episodes") return;
    if (searchTimerRef.current) clearTimeout(searchTimerRef.current);
    searchTimerRef.current = setTimeout(() => {
      setSearchBusy(true);
      setSearchStatus("");
      getScheduleBuilderCandidates(packageProfile, searchQuery.trim(), readyOnly ? "ready" : "all")
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
    if (groupBusy || showBusy) return;
    setGroupBusy(group);
    try {
      const media = await getScheduleBuilderGroup(group);
      const added = appendDraftEntries(media.map((m) => candidateToInsertItemFromMedia(m, isPlexRelay)), index);
      if (!displayName.trim()) setDisplayName(group);
      if (added === 0) setSearchStatus(`all episodes from "${group}" already in queue`);
    } catch (err) {
      setSearchStatus(err instanceof Error ? err.message : String(err));
    } finally {
      setGroupBusy(null);
    }
  }

  async function queueShow(show: MediaShow, index?: number) {
    if (showBusy || groupBusy) return;
    setShowBusy(show.showName);
    try {
      const groupNames = show.seasons.flatMap((season) => season.halves.map((h) => h.group));
      const batches = await Promise.all(groupNames.map((g) => getScheduleBuilderGroup(g)));
      const added = appendDraftEntries(batches.flat().map((m) => candidateToInsertItemFromMedia(m, isPlexRelay)), index);
      if (!displayName.trim()) setDisplayName(show.showName);
      if (added === 0) setSearchStatus(`all episodes from "${show.showName}" already in queue`);
    } catch (err) {
      setSearchStatus(err instanceof Error ? err.message : String(err));
    } finally {
      setShowBusy(null);
    }
  }

  async function queueAlbum(group: string, index?: number) {
    if (albumBusy || artistBusy) return;
    setAlbumBusy(group);
    try {
      const media = await getScheduleBuilderGroup(group);
      const added = appendDraftEntries(media.map((m) => candidateToInsertItemFromMedia(m, isPlexRelay)), index);
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
      const added = appendDraftEntries(batches.flat().map((m) => candidateToInsertItemFromMedia(m, isPlexRelay)), index);
      if (!displayName.trim()) setDisplayName(artistDisplayName);
      if (added === 0) setSearchStatus(`all tracks from "${artistDisplayName}" already in queue`);
    } catch (err) {
      setSearchStatus(err instanceof Error ? err.message : String(err));
    } finally {
      setArtistBusy(null);
    }
  }

  function toggleTab(tab: PickerTab) {
    setActiveTab((prev) => (prev === tab ? null : tab));
  }

  function insertMediaFromDrag(key: string, index: number) {
    const r = searchResults.find((x) => x.mediaId === key);
    if (r) appendDraftEntry(candidateToInsertItem(r, isPlexRelay), index);
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
    if (payload.kind === "show") {
      const show = shows.find((item) => item.showName === payload.showName);
      if (show) void queueShow(show, index);
      else setSearchStatus(`show not found: ${payload.showName}`);
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
      : isPlexRelay
        ? "Create Plex relay channel"
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
          <h2>Schedule Builder</h2>
          <p className="muted">checking media sources...</p>
        </section>
      </div>
    );
  }

  if (!existingMode && !sourceGate.hasMediaSource) {
    return (
      <div className="admin-panel">
        <section className="admin-panel-section">
          <h2>Schedule Builder</h2>
          <p className="muted">
            Connect at least one media source before building a schedule.
          </p>
          <div className={styles["sb-picker-actions"]}>
            {onOpenMediaSources && (
              <button type="button" className="primary" onClick={onOpenMediaSources}>
                Open Media Sources
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
    const meta = [r.schedulingGroup, isPlexRelay ? "" : packageStatusLabel(r, profileDetails)].filter(Boolean).join(" · ");
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
      <section className="admin-panel-section">
        <h2>{existingMode ? `Edit Schedule: ${displayName}` : "Schedule Builder"}</h2>
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
                  className={`${styles["sb-mode-btn"]}${playbackMode === "packaged" && !onDemand ? ` ${styles["is-active"]}` : ""}`}
                  onClick={() => {
                    setPlaybackMode("packaged");
                    setOnDemand(false);
                  }}
                >
                  Pre-encode
                </button>
                <button
                  type="button"
                  className={`${styles["sb-mode-btn"]}${playbackMode === "packaged" && onDemand ? ` ${styles["is-active"]}` : ""}`}
                  onClick={() => {
                    setPlaybackMode("packaged");
                    setOnDemand(true);
                    setAdaptiveBitrate("");
                  }}
                >
                  On-demand
                </button>
                <button
                  type="button"
                  className={`${styles["sb-mode-btn"]}${playbackMode === "plex_relay" ? ` ${styles["is-active"]}` : ""}`}
                  disabled={!sourceGate.plexConfigured}
                  title={sourceGate.plexConfigured ? undefined : "Connect Plex in Media Sources to enable relay playback."}
                  onClick={() => {
                    setPlaybackMode("plex_relay");
                    setOnDemand(false);
                    setAdaptiveBitrate("");
                  }}
                >
                  Plex relay
                </button>
              </div>
              <p className="muted">
                {playbackMode === "plex_relay"
                  ? "Playback follows this schedule but asks Plex for a per-viewer transcode session instead of using linearcast packages."
                  : !sourceGate.plexConfigured
                  ? "Connect Plex in Media Sources to enable Plex relay. Pre-encode and On-demand use linearcast packages."
                  : onDemand
                  ? "Nothing is encoded until someone tunes in. The first viewer waits while the current program encodes; later viewers join the live edge. Idle channels cost nothing."
                  : "Every program is packaged ahead of time so tune-in is instant."}
              </p>
              {playbackMode === "packaged" && !onDemand && channelMediaKind === "video" && (
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
              {isPlexRelay ? "Select a video profile to browse Plex media" : "Select a package profile to get started"}
            </span>
            <div className={styles["sb-profile-btns"]}>
              {profiles.map((p) => (
                <button
                  key={p}
                  type="button"
                  className={`sb-profile-btn${packageProfile === p ? " is-active" : ""}`}
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
          <div className={styles["sb-content-btns"]}>
            {channelMediaKind === "video" ? (
              <>
                <button
                  type="button"
                  className={`sb-content-btn${activeTab === "shows" ? " is-active" : ""}`}
                  onClick={() => toggleTab("shows")}
                >
                  Shows
                </button>
                <button
                  type="button"
                  className={`sb-content-btn${activeTab === "episodes" ? " is-active" : ""}`}
                  onClick={() => toggleTab("episodes")}
                >
                  Episodes
                </button>
              </>
            ) : (
              <button
                type="button"
                className={`sb-content-btn${activeTab === "music" ? " is-active" : ""}`}
                onClick={() => toggleTab("music")}
              >
                Music
              </button>
            )}
            {existingMode && (
              <button
                type="button"
                className={`sb-content-btn${activeTab === "filler" ? " is-active" : ""}`}
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
                  if (r) appendDraftEntry(candidateToInsertItem(r, isPlexRelay));
                }}
                toolsExtra={
                  isPlexRelay ? undefined : (
                    <label className={styles["sb-ready-filter"]}>
                      <input
                        type="checkbox"
                        checked={readyOnly}
                        onChange={(e) => setReadyOnly(e.target.checked)}
                      />
                      Ready only
                    </label>
                  )
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

          {activeTab === "shows" && (
            <div className={styles["sb-picker-expanded"]}>
              <SchedulePickerShowsGrid
                shows={shows}
                loading={showsLoading}
                error={showsError || undefined}
                filter={groupFilter}
                onFilterChange={setGroupFilter}
                selectedShow={selectedShow}
                onSelectShow={setSelectedShow}
                queueGroup={queueGroup}
                queueShow={queueShow}
                groupBusy={groupBusy}
                showBusy={showBusy}
                emptyMessage={
                  showsLoaded
                    ? shows.length === 0
                      ? "No media groups found. Run linearcast-ingest on a media directory first."
                      : "no shows match"
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

          {activeTab === "filler" && existingMode && (
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
                    disabled: !c.packageReady,
                  }))}
                onItemAction={() => {}}
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
                          <span className={styles["sb-entry-title"]}>{e.title || e.mediaId}</span>
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

function isABRProfile(profile?: PackageProfile): boolean {
  return (profile?.tags ?? []).includes("abr");
}

function profileChipLabel(name: string, details: Record<string, PackageProfile>) {
  if (!name) return "selected profile";
  return details[name]?.label || name;
}

// Converts the media shape from getScheduleBuilderGroup into a ScheduleInsertItem.
// getScheduleBuilderGroup returns a different type than MediaPackageCandidate (no packageStatus).
function candidateToInsertItemFromMedia(m: {
  mediaId: string;
  title?: string;
  path: string;
  schedulingGroup?: string;
  durationMs: number;
  packagedDurationMs?: number;
}, forceReady = false): ScheduleInsertItem {
  return {
    mediaId: m.mediaId,
    title: m.title,
    path: m.path,
    schedulingGroup: m.schedulingGroup,
    durationMs: m.packagedDurationMs ?? m.durationMs,
    packagedDurationMs: m.packagedDurationMs,
    packageReady: forceReady || m.packagedDurationMs != null,
    channelMember: false,
  };
}
