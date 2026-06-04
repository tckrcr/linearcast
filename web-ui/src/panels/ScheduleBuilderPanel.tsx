import { useEffect, useRef, useState } from "react";
import {
  getScheduleBuilderAlbums,
  getScheduleBuilderCandidates,
  getScheduleBuilderGroup,
  getScheduleBuilderProfileList,
  getScheduleBuilderShows,
} from "../api";
import type { MediaShow, MusicArtist } from "../api/media";
import { formatMs } from "../format";
import { useScheduleEditor } from "../hooks/useScheduleEditor";
import { useHasMediaSource } from "../hooks/useHasMediaSource";
import type { MediaPackageCandidate, PackageProfile, ScheduleInsertItem } from "../types";
import { MediaPickerRail } from "./MediaPickerRail";
import { SchedulePickerMusicGrid } from "./SchedulePickerMusicGrid";
import { SchedulePickerShowsGrid } from "./SchedulePickerShowsGrid";
import { ScheduleTimeline } from "./ScheduleTimeline";
import styles from "./ScheduleBuilderPanel.module.css";

type PickerTab = "episodes" | "shows" | "music";

function packageStatusLabel(candidate: MediaPackageCandidate, profileDetails: Record<string, PackageProfile>): string {
  if (candidate.packageStatus === "ready") return "";
  if (candidate.packageStatus === "missing") return "needs package";
  const profile = candidate.packageProfile || "";
  if (candidate.packageStatus === "failed") return `failed at ${profileChipLabel(profile, profileDetails)}`;
  return `${candidate.packageStatus} at ${profileChipLabel(profile, profileDetails)}`;
}

function candidateToInsertItem(r: MediaPackageCandidate): ScheduleInsertItem {
  return {
    mediaId: r.mediaId,
    title: r.title,
    path: r.path,
    schedulingGroup: r.schedulingGroup,
    durationMs: r.packagedDurationMs ?? r.durationMs,
    packagedDurationMs: r.packagedDurationMs,
    packageReady: r.packageStatus === "ready",
    channelMember: false,
  };
}

const BUILDER_CANDIDATE_LIMIT = 10;
const HOUR_MS = 3600 * 1000;

export function ScheduleBuilderPanel({
  onChannelImported,
  onOpenMediaSources,
}: {
  onChannelImported: () => void;
  onOpenMediaSources?: () => void;
}) {
  const sourceGate = useHasMediaSource();

  // Channel config state
  const [displayName, setDisplayName] = useState("");
  const [packageProfile, setPackageProfile] = useState("");
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

  // Entry management via the shared hook
  const {
    scheduleDraft,
    appendDraftEntries,
    appendDraftEntry,
    removeDraftEntry,
    clearScheduleDraft,
    moveDraftEntry,
    undoScheduleDraftChange,
    canUndoScheduleDraft,
    importDraftChannel,
    saveBusy,
    scheduleError,
    scheduleNotice,
  } = useScheduleEditor(null, { packageProfile, displayName, onImported: onChannelImported });

  // Profiles
  useEffect(() => {
    getScheduleBuilderProfileList()
      .then((next) => {
        setProfiles(next.profiles);
        setProfileDetails(Object.fromEntries(next.profileDetails.map((item) => [item.name, item])));
        setPackageProfile((current) => next.profiles.includes(current) ? current : "");
      })
      .catch(() => {});
  }, []);

  const channelMediaKind: "video" | "music" =
    profileDetails[packageProfile]?.mediaKind === "music" ? "music" : "video";

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

  async function queueGroup(group: string) {
    if (groupBusy || showBusy) return;
    setGroupBusy(group);
    try {
      const media = await getScheduleBuilderGroup(group);
      const added = appendDraftEntries(media.map(candidateToInsertItemFromMedia));
      if (!displayName.trim()) setDisplayName(group);
      if (added === 0) setSearchStatus(`all episodes from "${group}" already in queue`);
    } catch (err) {
      setSearchStatus(err instanceof Error ? err.message : String(err));
    } finally {
      setGroupBusy(null);
    }
  }

  async function queueShow(show: MediaShow) {
    if (showBusy || groupBusy) return;
    setShowBusy(show.showName);
    try {
      const groupNames = show.seasons.flatMap((season) => season.halves.map((h) => h.group));
      const batches = await Promise.all(groupNames.map((g) => getScheduleBuilderGroup(g)));
      const added = appendDraftEntries(batches.flat().map(candidateToInsertItemFromMedia));
      if (!displayName.trim()) setDisplayName(show.showName);
      if (added === 0) setSearchStatus(`all episodes from "${show.showName}" already in queue`);
    } catch (err) {
      setSearchStatus(err instanceof Error ? err.message : String(err));
    } finally {
      setShowBusy(null);
    }
  }

  async function queueAlbum(group: string) {
    if (albumBusy || artistBusy) return;
    setAlbumBusy(group);
    try {
      const media = await getScheduleBuilderGroup(group);
      const added = appendDraftEntries(media.map(candidateToInsertItemFromMedia));
      if (!displayName.trim()) setDisplayName(group);
      if (added === 0) setSearchStatus(`all tracks from "${group}" already in queue`);
    } catch (err) {
      setSearchStatus(err instanceof Error ? err.message : String(err));
    } finally {
      setAlbumBusy(null);
    }
  }

  async function queueArtist(artist: MusicArtist) {
    if (albumBusy || artistBusy) return;
    setArtistBusy(artist.artistName);
    try {
      const batches = await Promise.all(artist.albums.map((al) => getScheduleBuilderGroup(al.group)));
      const artistDisplayName = artist.artistName || "Unknown Artist";
      const added = appendDraftEntries(batches.flat().map(candidateToInsertItemFromMedia));
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

  const totalMs = scheduleDraft.reduce((sum, e) => sum + e.durationMs, 0);
  const timelineEntries = scheduleDraft.reduce<typeof scheduleDraft>((entries, entry) => {
    const startMs = entries.length === 0 ? 0 : entries[entries.length - 1].endMs;
    entries.push({ ...entry, startMs, endMs: startMs + entry.durationMs });
    return entries;
  }, []);
  const timelineWindowHours = Math.max(1, Math.ceil(Math.max(totalMs, 1) / HOUR_MS));
  const selectedMediaKeys = new Set(
    scheduleDraft.flatMap((e) => [e.mediaId, e.path].filter(Boolean) as string[]),
  );
  const allKnownReady = scheduleDraft.length > 0 && scheduleDraft.every((e) => e.needsPackage !== true);
  const importButtonLabel = saveBusy
    ? "Importing..."
    : allKnownReady
      ? "Create channel"
      : "Create channel and queue packages";
  const statusMessage = scheduleError || scheduleNotice;

  // ---------------------------------------------------------------------------
  // Source gate
  // ---------------------------------------------------------------------------

  if (sourceGate.loading) {
    return (
      <div className="admin-panel">
        <section className="admin-panel-section">
          <h2>Schedule Builder</h2>
          <p className="muted">checking media sources...</p>
        </section>
      </div>
    );
  }

  if (!sourceGate.hasMediaSource) {
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

  const visibleSearchResults = searchResults.slice(0, BUILDER_CANDIDATE_LIMIT);
  const episodeItems = visibleSearchResults.map((r) => {
    const isSelected = selectedMediaKeys.has(r.mediaId) || selectedMediaKeys.has(r.path);
    const meta = [r.schedulingGroup, packageStatusLabel(r, profileDetails)].filter(Boolean).join(" · ");
    return {
      key: r.mediaId,
      title: r.title || r.path.split("/").pop() || r.path,
      meta: meta || undefined,
      durationMs: r.packagedDurationMs ?? r.durationMs,
      disabled: isSelected,
      actionLabel: isSelected ? "Added" : "Add",
    };
  });

  // ---------------------------------------------------------------------------
  // Render
  // ---------------------------------------------------------------------------

  return (
    <div className="admin-panel sb-panel">
      <section className="admin-panel-section">
        <h2>Schedule Builder</h2>
        <div className={styles["sb-config"]}>
          <label className={styles["sb-name-label"]}>
            <span>display name</span>
            <input
              value={displayName}
              placeholder="My Channel"
              onChange={(e) => setDisplayName(e.target.value)}
            />
          </label>
          <div className={styles["sb-profile-field"]}>
            <span className={styles["sb-field-label"]}>Select a package profile to get started</span>
            <div className={styles["sb-profile-btns"]}>
              {profiles.map((p) => (
                <button
                  key={p}
                  type="button"
                  className={`sb-profile-btn${packageProfile === p ? " is-active" : ""}`}
                  title={p}
                  onClick={() => setPackageProfile(p === packageProfile ? "" : p)}
                >
                  {profileDetails[p]?.label ?? p}
                </button>
              ))}
            </div>
          </div>
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
                onItemAction={(key) => {
                  const r = searchResults.find((x) => x.mediaId === key);
                  if (r) appendDraftEntry(candidateToInsertItem(r));
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
                  searchQuery.trim() && searchResults.length === 0 ? "no candidates match" : undefined
                }
                footer={
                  !searchBusy && !searchStatus && searchResults.length > BUILDER_CANDIDATE_LIMIT ? (
                    <span className="muted">
                      Showing first {BUILDER_CANDIDATE_LIMIT} of {searchResults.length}; keep typing to narrow.
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

          <div className="section-headline sb-queue-headline">
            <h3>
              {channelMediaKind === "music" ? "Tracks" : "Queue"}
              {scheduleDraft.length > 0 && (
                <span className="muted sb-list-meta">
                  {" "}({scheduleDraft.length} · {formatMs(totalMs)})
                </span>
              )}
            </h3>
            {scheduleDraft.length > 0 && (
              <button type="button" className="danger" onClick={clearScheduleDraft}>
                Clear all
              </button>
            )}
            <button type="button" disabled={!canUndoScheduleDraft} onClick={undoScheduleDraftChange}>
              Undo
            </button>
          </div>

          {scheduleDraft.length === 0 ? (
            <p className="muted sb-empty">
              {channelMediaKind === "music"
                ? "No tracks yet — open Music above and queue an album."
                : "No episodes yet — open Shows or Episodes above."}
            </p>
          ) : (
            <>
              <ScheduleTimeline
                windowStartMs={0}
                windowHours={timelineWindowHours}
                nowMs={-1}
                entries={timelineEntries}
                unanchored
                disabled
              />
              <ul className={styles["sb-entry-list"]}>
                {scheduleDraft.map((e, index) => (
                  <li key={e.draftId} className={styles["sb-entry"]}>
                    <span className={styles["sb-entry-title"]}>{e.title || e.mediaId}</span>
                    <span className="sb-entry-duration muted">{formatMs(e.durationMs)}</span>
                    <div className={styles["sb-entry-move"]}>
                      <button type="button" disabled={index === 0} onClick={() => moveDraftEntry(index, index - 1)} aria-label="Move up">↑</button>
                      <button type="button" disabled={index === scheduleDraft.length - 1} onClick={() => moveDraftEntry(index, index + 1)} aria-label="Move down">↓</button>
                    </div>
                    <button type="button" className={styles["sb-entry-remove"]} aria-label="Remove" onClick={() => removeDraftEntry(index)}>✕</button>
                  </li>
                ))}
              </ul>
            </>
          )}

          <div className={styles["sb-import-row"]}>
            <button
              type="button"
              className="primary"
              disabled={saveBusy || scheduleDraft.length === 0 || !displayName.trim()}
              onClick={() => void importDraftChannel()}
              title="Create channel and queue any unpackaged media."
            >
              {importButtonLabel}
            </button>
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

// Converts the media shape from getScheduleBuilderGroup into a ScheduleInsertItem.
// getScheduleBuilderGroup returns a different type than MediaPackageCandidate (no packageStatus).
function candidateToInsertItemFromMedia(m: {
  mediaId: string;
  title?: string;
  path: string;
  schedulingGroup?: string;
  durationMs: number;
  packagedDurationMs?: number;
}): ScheduleInsertItem {
  return {
    mediaId: m.mediaId,
    title: m.title,
    path: m.path,
    schedulingGroup: m.schedulingGroup,
    durationMs: m.packagedDurationMs ?? m.durationMs,
    packagedDurationMs: m.packagedDurationMs,
    packageReady: m.packagedDurationMs != null,
    channelMember: false,
  };
}
