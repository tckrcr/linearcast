import { useMemo } from "react";
import { SCHEDULE_BATCH_DRAG_MIME } from "../constants";
import { formatMs } from "../format";
import type { MediaShow, MediaShowHalf, MediaShowSeason } from "../api/media";
import styles from "./ScheduleBuilderPanel.module.css";
import mpStyles from "./MediaPickerRail.module.css";

type Props = {
  shows: MediaShow[];
  loading: boolean;
  error?: string;
  filter: string;
  onFilterChange: (q: string) => void;
  selectedShow: MediaShow | null;
  onSelectShow: (show: MediaShow | null) => void;
  queueGroup: (group: string) => void;
  queueShow: (show: MediaShow) => void;
  groupBusy: string | null;
  showBusy: string | null;
  emptyMessage?: string;
};

export function SchedulePickerShowsGrid({
  shows,
  loading,
  error,
  filter,
  onFilterChange,
  selectedShow,
  onSelectShow,
  queueGroup,
  queueShow,
  groupBusy,
  showBusy,
  emptyMessage,
}: Props) {
  const filterText = filter.trim().toLowerCase();
  const filteredShows = useMemo(
    () => (filterText ? shows.filter((s) => s.showName.toLowerCase().includes(filterText)) : shows),
    [filterText, shows],
  );

  if (selectedShow) {
    const fresh = shows.find((s) => s.showName === selectedShow.showName) ?? selectedShow;
    return (
      <ShowDetail
        show={fresh}
        onBack={() => onSelectShow(null)}
        queueGroup={queueGroup}
        queueShow={queueShow}
        groupBusy={groupBusy}
        showBusy={showBusy}
      />
    );
  }

  return (
    <div className={styles["sb-shows-picker"]}>
      <div className={mpStyles["mp-rail-tools"]}>
        <input
          className={mpStyles["mp-rail-input"]}
          value={filter}
          placeholder="Filter shows"
          onChange={(e) => onFilterChange(e.target.value)}
        />
      </div>
      {loading && <p className={`muted ${mpStyles["mp-rail-status"]}`}>loading shows…</p>}
      {!loading && error && <p className={`error ${mpStyles["mp-rail-status"]}`}>{error}</p>}
      {!loading && !error && filteredShows.length === 0 && (
        <p className={`muted ${mpStyles["mp-rail-empty"]}`}>{emptyMessage ?? "no shows match"}</p>
      )}
      {!loading && !error && filteredShows.length > 0 && (
        <div className={styles["sb-shows-grid"]}>
          {filteredShows.map((show) => (
            <button
              key={show.showName}
              type="button"
              className={styles["sb-show-card"]}
              onClick={() => onSelectShow(show)}
              aria-label={`Open ${show.showName}`}
              draggable
              onDragStart={(e) => {
                e.dataTransfer.effectAllowed = "copy";
                e.dataTransfer.setData(SCHEDULE_BATCH_DRAG_MIME, JSON.stringify({ kind: "show", showName: show.showName }));
                e.dataTransfer.setData("text/plain", show.showName);
              }}
            >
              <ShowPosterPlaceholder name={show.showName} />
              <div className={styles["sb-show-card-meta"]}>
                <span className={styles["sb-show-card-title"]}>{show.showName}</span>
                <span className="muted sb-show-card-sub">
                  {show.episodeCount} ep{show.episodeCount === 1 ? "" : "s"} · {show.seasonCount} season
                  {show.seasonCount === 1 ? "" : "s"}
                </span>
              </div>
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

function ShowDetail({
  show,
  onBack,
  queueGroup,
  queueShow,
  groupBusy,
  showBusy,
}: {
  show: MediaShow;
  onBack: () => void;
  queueGroup: (group: string) => void;
  queueShow: (show: MediaShow) => void;
  groupBusy: string | null;
  showBusy: string | null;
}) {
  const queueingShow = showBusy === show.showName;
  return (
    <div className={styles["sb-show-detail"]}>
      <div className={styles["sb-show-detail-head"]}>
        <button type="button" onClick={onBack} className={styles["sb-show-back"]}>
          ← Shows
        </button>
        <div className={styles["sb-show-detail-title"]}>
          <h4>{show.showName}</h4>
          <span className="muted">
            {show.episodeCount} ep{show.episodeCount === 1 ? "" : "s"} · {show.seasonCount} season
            {show.seasonCount === 1 ? "" : "s"} · {formatMs(show.durationMs)}
          </span>
        </div>
        <button
          type="button"
          className="primary"
          disabled={queueingShow || groupBusy !== null}
          draggable={!(queueingShow || groupBusy !== null)}
          onDragStart={(e) => {
            e.dataTransfer.effectAllowed = "copy";
            e.dataTransfer.setData(SCHEDULE_BATCH_DRAG_MIME, JSON.stringify({ kind: "show", showName: show.showName }));
            e.dataTransfer.setData("text/plain", show.showName);
          }}
          onClick={() => queueShow(show)}
        >
          {queueingShow ? "Queueing…" : "Queue entire show"}
        </button>
      </div>
      <ul className={styles["sb-show-seasons"]}>
        {show.seasons.map((season) => (
          <SeasonRow
            key={season.season}
            season={season}
            queueGroup={queueGroup}
            groupBusy={groupBusy}
            disabled={queueingShow}
          />
        ))}
      </ul>
    </div>
  );
}

function SeasonRow({
  season,
  queueGroup,
  groupBusy,
  disabled,
}: {
  season: MediaShowSeason;
  queueGroup: (group: string) => void;
  groupBusy: string | null;
  disabled: boolean;
}) {
  return (
    <li className={styles["sb-show-season"]}>
      <div className={styles["sb-show-season-head"]}>
        <span className={styles["sb-show-season-label"]}>Season {season.season}</span>
        <span className="muted sb-show-season-sub">
          {season.episodeCount} ep{season.episodeCount === 1 ? "" : "s"} · {formatMs(season.durationMs)}
        </span>
      </div>
      <ul className={styles["sb-show-halves"]}>
        {season.halves.map((half) => (
          <HalfRow
            key={half.half}
            half={half}
            queueGroup={queueGroup}
            groupBusy={groupBusy}
            disabled={disabled}
          />
        ))}
      </ul>
    </li>
  );
}

function HalfRow({
  half,
  queueGroup,
  groupBusy,
  disabled,
}: {
  half: MediaShowHalf;
  queueGroup: (group: string) => void;
  groupBusy: string | null;
  disabled: boolean;
}) {
  const busy = groupBusy === half.group;
  return (
    <li
      className={styles["sb-show-half"]}
      draggable={!(busy || disabled || (groupBusy !== null && !busy))}
      onDragStart={(e) => {
        e.dataTransfer.effectAllowed = "copy";
        e.dataTransfer.setData(SCHEDULE_BATCH_DRAG_MIME, JSON.stringify({ kind: "group", group: half.group }));
        e.dataTransfer.setData("text/plain", half.group);
      }}
    >
      <span className={styles["sb-show-half-label"]}>H{half.half}</span>
      <span className="muted sb-show-half-sub">
        {half.episodeCount} ep{half.episodeCount === 1 ? "" : "s"} · {formatMs(half.durationMs)}
      </span>
      <button
        type="button"
        disabled={busy || disabled || (groupBusy !== null && !busy)}
        onClick={() => queueGroup(half.group)}
      >
        {busy ? "Queueing…" : "Queue"}
      </button>
    </li>
  );
}

function ShowPosterPlaceholder({ name }: { name: string }) {
  const initials = useMemo(() => deriveInitials(name), [name]);
  const hue = useMemo(() => hashHue(name), [name]);
  const bg = `hsl(${hue}, 38%, 22%)`;
  const accent = `hsl(${(hue + 30) % 360}, 38%, 38%)`;
  return (
    <div className={styles["sb-show-poster"]} aria-hidden="true">
      <div className={styles["sb-show-poster-art"]} style={{ background: bg }}>
        <div className={styles["sb-show-poster-band"]} style={{ background: accent }} />
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

function hashHue(s: string): number {
  let h = 0;
  for (let i = 0; i < s.length; i++) {
    h = (h * 31 + s.charCodeAt(i)) | 0;
  }
  return Math.abs(h) % 360;
}
