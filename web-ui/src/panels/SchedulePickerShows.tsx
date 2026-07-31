import { useMemo, useState, type ReactNode } from "react";
import { MEDIA_DRAG_MIME, SCHEDULE_BATCH_DRAG_MIME } from "../constants";
import { formatMs } from "../format";
import type { MediaInventoryItem, MediaShow } from "../api/media";
import type { PickerRailItem } from "./MediaPickerRail";
import styles from "./ScheduleBuilderPanel.module.css";
import mpStyles from "./MediaPickerRail.module.css";

type Props = {
  shows: MediaShow[];
  showsLoading: boolean;
  showsError?: string;
  query: string;
  onQueryChange: (query: string) => void;
  searchLoading: boolean;
  searchError?: string;
  toolsExtra?: ReactNode;
  episodeItems: PickerRailItem[];
  onAddEpisode: (mediaId: string) => void;
  selectedShow: MediaShow | null;
  onSelectShow: (show: MediaShow | null) => void;
  showEpisodes: MediaInventoryItem[];
  showEpisodesLoading: boolean;
  showEpisodesError?: string;
  selectedEpisodeKeys: ReadonlySet<string>;
  onQueueShow: (show: MediaShow) => void;
  onQueueSeason: (show: MediaShow, seasonNumber: number | null) => void;
  onQueueEpisode: (episode: MediaInventoryItem) => void;
  groupBusy: string | null;
};

export function SchedulePickerShows({
  shows,
  showsLoading,
  showsError,
  query,
  onQueryChange,
  searchLoading,
  searchError,
  toolsExtra,
  episodeItems,
  onAddEpisode,
  selectedShow,
  onSelectShow,
  showEpisodes,
  showEpisodesLoading,
  showEpisodesError,
  selectedEpisodeKeys,
  onQueueShow,
  onQueueSeason,
  onQueueEpisode,
  groupBusy,
}: Props) {
  const filterText = normalizeForMatch(query);
  const matchingShows = useMemo(
    () =>
      filterText
        ? shows.filter((show) => normalizeForMatch(show.name).includes(filterText))
        : shows,
    [filterText, shows],
  );

  if (selectedShow) {
    return (
      <ShowDetail
        show={selectedShow}
        episodes={showEpisodes}
        loading={showEpisodesLoading}
        error={showEpisodesError}
        busy={groupBusy === selectedShow.name}
        onBack={() => onSelectShow(null)}
        onQueueShow={() => onQueueShow(selectedShow)}
        onQueueSeason={(seasonNumber) => onQueueSeason(selectedShow, seasonNumber)}
        onQueueEpisode={onQueueEpisode}
        selectedEpisodeKeys={selectedEpisodeKeys}
      />
    );
  }

  const noResults =
    !showsLoading &&
    !searchLoading &&
    !showsError &&
    !searchError &&
    matchingShows.length === 0 &&
    episodeItems.length === 0;

  return (
    <div className={styles["sb-shows-picker"]}>
      <div className={mpStyles["mp-rail-tools"]}>
        <input
          autoFocus
          aria-label="Search shows and episodes"
          className={mpStyles["mp-rail-input"]}
          value={query}
          placeholder="Search shows or individual episodes…"
          onChange={(event) => onQueryChange(event.target.value)}
        />
        {toolsExtra}
      </div>

      {showsLoading && <p className={`muted ${mpStyles["mp-rail-status"]}`}>loading shows…</p>}
      {!showsLoading && showsError && <p className={`error ${mpStyles["mp-rail-status"]}`}>{showsError}</p>}
      {!searchLoading && searchError && <p className={`error ${mpStyles["mp-rail-status"]}`}>{searchError}</p>}

      {!showsLoading && !showsError && matchingShows.length > 0 && (
        <section className={styles["sb-show-results"]} aria-labelledby="show-results-heading">
          <div className={styles["sb-result-heading"]}>
            <h4 id="show-results-heading">Shows</h4>
            <span className="muted">{matchingShows.length} collection{matchingShows.length === 1 ? "" : "s"}</span>
          </div>
          <div className={styles["sb-shows-grid"]}>
            {matchingShows.map((show) => (
              <button
                key={show.collectionId}
                type="button"
                className={styles["sb-show-card"]}
                aria-label={`Open ${show.name}`}
                draggable
                onClick={() => onSelectShow(show)}
                onDragStart={(event) => {
                  event.dataTransfer.effectAllowed = "copy";
                  event.dataTransfer.setData(
                    SCHEDULE_BATCH_DRAG_MIME,
                    JSON.stringify({ kind: "group", group: show.name }),
                  );
                  event.dataTransfer.setData("text/plain", show.name);
                }}
              >
                <ShowPosterPlaceholder name={show.name} />
                <div className={styles["sb-show-card-meta"]}>
                  <span className={styles["sb-show-card-title"]}>{show.name}</span>
                  <span className={`muted ${styles["sb-show-card-sub"]}`}>
                    {show.seasonCount} season{show.seasonCount === 1 ? "" : "s"} ·{" "}
                    {show.episodeCount} episode{show.episodeCount === 1 ? "" : "s"}
                  </span>
                </div>
              </button>
            ))}
          </div>
        </section>
      )}

      {filterText && (
        <section className={styles["sb-show-results"]} aria-labelledby="episode-results-heading">
          <div className={styles["sb-result-heading"]}>
            <h4 id="episode-results-heading">Individual episodes</h4>
            {searchLoading && <span className="muted">searching…</span>}
          </div>
          {!searchLoading && episodeItems.length > 0 && (
            <ul className={mpStyles["mp-rail-list"]}>
              {episodeItems.map((item) => (
                <li key={item.key} className={`${mpStyles["mp-rail-row"]} is-draggable`}>
                  <span
                    className={mpStyles["mp-rail-row-grip"]}
                    draggable
                    title="Drag onto the timeline"
                    onDragStart={(event) => {
                      event.dataTransfer.effectAllowed = "copy";
                      event.dataTransfer.setData(MEDIA_DRAG_MIME, item.key);
                      event.dataTransfer.setData("text/plain", item.title);
                    }}
                  >
                    ⠿
                  </span>
                  <div className={mpStyles["mp-rail-row-main"]}>
                    <span className={mpStyles["mp-rail-row-title"]}>{item.title}</span>
                    {item.meta != null && (
                      <span className={`muted ${mpStyles["mp-rail-row-meta"]}`}>{item.meta}</span>
                    )}
                  </div>
                  {item.durationMs != null && (
                    <span className={`muted ${mpStyles["mp-rail-row-dur"]}`}>{formatMs(item.durationMs)}</span>
                  )}
                  <button
                    type="button"
                    className={`primary ${mpStyles["mp-rail-row-add"]}`}
                    onClick={() => onAddEpisode(item.key)}
                  >
                    Add
                  </button>
                </li>
              ))}
            </ul>
          )}
          {!searchLoading && episodeItems.length === 0 && matchingShows.length > 0 && !searchError && (
            <p className={`muted ${mpStyles["mp-rail-empty"]}`}>
              No individual episode matches; open a show above to browse its seasons.
            </p>
          )}
        </section>
      )}

      {noResults && <p className={`muted ${mpStyles["mp-rail-empty"]}`}>No shows or episodes match.</p>}
    </div>
  );
}

function ShowDetail({
  show,
  episodes,
  loading,
  error,
  busy,
  onBack,
  onQueueShow,
  onQueueSeason,
  onQueueEpisode,
  selectedEpisodeKeys,
}: {
  show: MediaShow;
  episodes: MediaInventoryItem[];
  loading: boolean;
  error?: string;
  busy: boolean;
  onBack: () => void;
  onQueueShow: () => void;
  onQueueSeason: (seasonNumber: number | null) => void;
  onQueueEpisode: (episode: MediaInventoryItem) => void;
  selectedEpisodeKeys: ReadonlySet<string>;
}) {
  const [expandedSeasons, setExpandedSeasons] = useState<Set<number | null>>(new Set());
  const episodesBySeason = useMemo(() => {
    const grouped = new Map<number | null, MediaInventoryItem[]>();
    for (const episode of episodes) {
      const seasonNumber = episode.seasonNumber ?? null;
      grouped.set(seasonNumber, [...(grouped.get(seasonNumber) ?? []), episode]);
    }
    for (const seasonEpisodes of grouped.values()) {
      seasonEpisodes.sort(
        (left, right) =>
          (left.episodeNumber ?? Number.MAX_SAFE_INTEGER) -
            (right.episodeNumber ?? Number.MAX_SAFE_INTEGER) ||
          left.title.localeCompare(right.title),
      );
    }
    return grouped;
  }, [episodes]);

  return (
    <div className={styles["sb-show-detail"]}>
      <div className={styles["sb-show-detail-head"]}>
        <button type="button" onClick={onBack} className={styles["sb-show-back"]}>
          ← Shows
        </button>
        <div className={styles["sb-show-detail-title"]}>
          <h4>{show.name}</h4>
          <span className="muted">
            {show.seasonCount} season{show.seasonCount === 1 ? "" : "s"} ·{" "}
            {show.episodeCount} episode{show.episodeCount === 1 ? "" : "s"} · {formatMs(show.durationMs)}
          </span>
        </div>
        <button
          type="button"
          className="primary"
          disabled={busy || loading || episodes.length === 0}
          onClick={onQueueShow}
        >
          {busy ? "Adding…" : "Add all"}
        </button>
      </div>
      {loading && <p className="muted">loading episodes…</p>}
      {!loading && error && <p className="error">{error}</p>}
      {!loading && !error && episodes.length === 0 && <p className="muted">No schedulable episodes found.</p>}
      {!loading && !error && episodes.length > 0 && (
        <ul className={styles["sb-show-seasons"]}>
          {show.seasons.map((season) => {
            const seasonNumber = season.seasonNumber ?? null;
            const seasonEpisodes = episodesBySeason.get(seasonNumber) ?? [];
            const seasonLabel = seasonNumber == null ? "Other episodes" : `Season ${seasonNumber}`;
            const expanded = expandedSeasons.has(seasonNumber);
            return (
              <li key={seasonNumber ?? "unsorted"} className={styles["sb-show-season"]}>
                <div className={styles["sb-show-season-head"]}>
                  <button
                    type="button"
                    className={styles["sb-show-season-toggle"]}
                    aria-label={`${expanded ? "Collapse" : "Expand"} ${seasonLabel}`}
                    aria-expanded={expanded}
                    onClick={() =>
                      setExpandedSeasons((current) => {
                        const next = new Set(current);
                        if (expanded) next.delete(seasonNumber);
                        else next.add(seasonNumber);
                        return next;
                      })
                    }
                  >
                    <span className={styles["sb-show-season-chevron"]} aria-hidden="true">
                      {expanded ? "▾" : "▸"}
                    </span>
                    <span className={styles["sb-show-season-label"]}>{seasonLabel}</span>
                    <span className={`muted ${styles["sb-show-season-sub"]}`}>
                      {seasonEpisodes.length} episode{seasonEpisodes.length === 1 ? "" : "s"} ·{" "}
                      {formatMs(season.durationMs)}
                    </span>
                  </button>
                  <button
                    type="button"
                    className={styles["sb-show-season-add"]}
                    disabled={busy || seasonEpisodes.length === 0}
                    onClick={() => onQueueSeason(seasonNumber)}
                  >
                    Add season
                  </button>
                </div>
                {expanded && (
                  <ul className={styles["sb-show-episodes"]} aria-label={`${seasonLabel} episodes`}>
                    {seasonEpisodes.map((episode) => {
                      const added =
                        selectedEpisodeKeys.has(episode.mediaId) ||
                        (episode.path !== "" && selectedEpisodeKeys.has(episode.path));
                      return (
                        <li key={episode.mediaId} className={styles["sb-show-episode"]}>
                          <div className={styles["sb-show-episode-main"]}>
                            <span className={styles["sb-show-episode-title"]}>
                              {episode.title || episode.path.split("/").pop() || episode.path}
                            </span>
                            {episode.episodeCode && (
                              <span className={`muted ${styles["sb-show-episode-code"]}`}>
                                {episode.episodeCode}
                              </span>
                            )}
                          </div>
                          <span className={`muted ${styles["sb-show-episode-duration"]}`}>
                            {formatMs(episode.packagedDurationMs ?? episode.durationMs)}
                          </span>
                          <button
                            type="button"
                            className={added ? undefined : "primary"}
                            disabled={added}
                            onClick={() => onQueueEpisode(episode)}
                          >
                            {added ? "Added" : "Add"}
                          </button>
                        </li>
                      );
                    })}
                  </ul>
                )}
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
}

function ShowPosterPlaceholder({ name }: { name: string }) {
  const initials = useMemo(() => deriveInitials(name), [name]);
  const hue = useMemo(() => hashHue(name), [name]);
  return (
    <div className={styles["sb-show-poster"]} aria-hidden="true">
      <div className={styles["sb-show-poster-art"]} style={{ background: `hsl(${hue}, 38%, 22%)` }}>
        <div
          className={styles["sb-show-poster-band"]}
          style={{ background: `hsl(${(hue + 30) % 360}, 38%, 38%)` }}
        />
        <span className={styles["sb-show-poster-initials"]}>{initials}</span>
      </div>
    </div>
  );
}

function deriveInitials(name: string): string {
  const words = name.replace(/[-_]+/g, " ").split(/\s+/).filter(Boolean);
  if (words.length === 0) return "?";
  if (words.length === 1) return words[0].slice(0, 2).toUpperCase();
  return (words[0][0] + words[1][0]).toUpperCase();
}

function hashHue(value: string): number {
  let hash = 0;
  for (let index = 0; index < value.length; index++) {
    hash = (hash * 31 + value.charCodeAt(index)) | 0;
  }
  return Math.abs(hash) % 360;
}

function normalizeForMatch(value: string): string {
  return value.toLowerCase().replace(/[^a-z0-9]+/g, " ").trim();
}
