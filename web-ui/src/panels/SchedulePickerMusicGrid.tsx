import { useMemo } from "react";
import { SCHEDULE_BATCH_DRAG_MIME } from "../constants";
import { formatMs } from "../format";
import type { MusicAlbum, MusicArtist } from "../api/media";
import styles from "./ScheduleBuilderPanel.module.css";
import mpStyles from "./MediaPickerRail.module.css";

type Props = {
  artists: MusicArtist[];
  loading: boolean;
  error?: string;
  filter: string;
  onFilterChange: (q: string) => void;
  selectedArtist: MusicArtist | null;
  onSelectArtist: (artist: MusicArtist | null) => void;
  queueAlbum: (group: string) => void;
  queueArtist: (artist: MusicArtist) => void;
  albumBusy: string | null;
  artistBusy: string | null;
  emptyMessage?: string;
};

export function SchedulePickerMusicGrid({
  artists,
  loading,
  error,
  filter,
  onFilterChange,
  selectedArtist,
  onSelectArtist,
  queueAlbum,
  queueArtist,
  albumBusy,
  artistBusy,
  emptyMessage,
}: Props) {
  const filterText = filter.trim().toLowerCase();
  const filteredArtists = useMemo(
    () =>
      filterText
        ? artists.filter(
            (a) =>
              (a.artistName || "Unknown Artist").toLowerCase().includes(filterText) ||
              a.albums.some((al) => al.albumName.toLowerCase().includes(filterText)),
          )
        : artists,
    [filterText, artists],
  );

  if (selectedArtist) {
    const fresh = artists.find((a) => a.artistName === selectedArtist.artistName) ?? selectedArtist;
    return (
      <ArtistDetail
        artist={fresh}
        onBack={() => onSelectArtist(null)}
        queueAlbum={queueAlbum}
        queueArtist={queueArtist}
        albumBusy={albumBusy}
        artistBusy={artistBusy}
      />
    );
  }

  return (
    <div className={styles["sb-shows-picker"]}>
      <div className={mpStyles["mp-rail-tools"]}>
        <input
          className={mpStyles["mp-rail-input"]}
          value={filter}
          placeholder="Filter artists or albums"
          onChange={(e) => onFilterChange(e.target.value)}
        />
      </div>
      {loading && <p className={`muted ${mpStyles["mp-rail-status"]}`}>loading albums…</p>}
      {!loading && error && <p className={`error ${mpStyles["mp-rail-status"]}`}>{error}</p>}
      {!loading && !error && filteredArtists.length === 0 && (
        <p className={`muted ${mpStyles["mp-rail-empty"]}`}>
          {emptyMessage ?? "no artists match"}
        </p>
      )}
      {!loading && !error && filteredArtists.length > 0 && (
        <div className={styles["sb-shows-grid"]}>
          {filteredArtists.map((artist) => {
            const displayName = artist.artistName || "Unknown Artist";
            return (
              <button
                key={artist.artistName || "__unknown__"}
                type="button"
                className={styles["sb-show-card"]}
                onClick={() => onSelectArtist(artist)}
                aria-label={`Open ${displayName}`}
                draggable
                onDragStart={(e) => {
                  e.dataTransfer.effectAllowed = "copy";
                  e.dataTransfer.setData(SCHEDULE_BATCH_DRAG_MIME, JSON.stringify({ kind: "artist", artistName: artist.artistName }));
                  e.dataTransfer.setData("text/plain", displayName);
                }}
              >
                <ArtistPosterPlaceholder name={displayName} />
                <div className={styles["sb-show-card-meta"]}>
                  <span className={styles["sb-show-card-title"]}>{displayName}</span>
                  <span className="muted sb-show-card-sub">
                    {artist.albumCount} album{artist.albumCount === 1 ? "" : "s"} ·{" "}
                    {artist.trackCount} track{artist.trackCount === 1 ? "" : "s"}
                  </span>
                </div>
              </button>
            );
          })}
        </div>
      )}
    </div>
  );
}

function ArtistDetail({
  artist,
  onBack,
  queueAlbum,
  queueArtist,
  albumBusy,
  artistBusy,
}: {
  artist: MusicArtist;
  onBack: () => void;
  queueAlbum: (group: string) => void;
  queueArtist: (artist: MusicArtist) => void;
  albumBusy: string | null;
  artistBusy: string | null;
}) {
  const displayName = artist.artistName || "Unknown Artist";
  const queuingArtist = artistBusy === artist.artistName;
  return (
    <div className={styles["sb-show-detail"]}>
      <div className={styles["sb-show-detail-head"]}>
        <button type="button" onClick={onBack} className={styles["sb-show-back"]}>
          ← Artists
        </button>
        <div className={styles["sb-show-detail-title"]}>
          <h4>{displayName}</h4>
          <span className="muted">
            {artist.albumCount} album{artist.albumCount === 1 ? "" : "s"} ·{" "}
            {artist.trackCount} track{artist.trackCount === 1 ? "" : "s"} ·{" "}
            {formatMs(artist.durationMs)}
          </span>
        </div>
        <button
          type="button"
          className="primary"
          disabled={queuingArtist || albumBusy !== null}
          draggable={!(queuingArtist || albumBusy !== null)}
          onDragStart={(e) => {
            e.dataTransfer.effectAllowed = "copy";
            e.dataTransfer.setData(SCHEDULE_BATCH_DRAG_MIME, JSON.stringify({ kind: "artist", artistName: artist.artistName }));
            e.dataTransfer.setData("text/plain", displayName);
          }}
          onClick={() => queueArtist(artist)}
        >
          {queuingArtist ? "Queueing…" : "Queue all"}
        </button>
      </div>
      <ul className={styles["sb-show-seasons"]}>
        {artist.albums.map((album) => (
          <AlbumRow
            key={album.group}
            album={album}
            queueAlbum={queueAlbum}
            albumBusy={albumBusy}
            disabled={queuingArtist}
          />
        ))}
      </ul>
    </div>
  );
}

function AlbumRow({
  album,
  queueAlbum,
  albumBusy,
  disabled,
}: {
  album: MusicAlbum;
  queueAlbum: (group: string) => void;
  albumBusy: string | null;
  disabled: boolean;
}) {
  const busy = albumBusy === album.group;
  return (
    <li
      className={styles["sb-show-season"]}
      draggable={!(busy || disabled || (albumBusy !== null && !busy))}
      onDragStart={(e) => {
        e.dataTransfer.effectAllowed = "copy";
        e.dataTransfer.setData(SCHEDULE_BATCH_DRAG_MIME, JSON.stringify({ kind: "album", group: album.group }));
        e.dataTransfer.setData("text/plain", album.albumName);
      }}
    >
      <div className={styles["sb-show-season-head"]}>
        <span className={styles["sb-show-season-label"]}>{album.albumName}</span>
        <span className="muted sb-show-season-sub">
          {album.trackCount} track{album.trackCount === 1 ? "" : "s"} · {formatMs(album.durationMs)}
        </span>
        <button
          type="button"
          disabled={busy || disabled || (albumBusy !== null && !busy)}
          onClick={() => queueAlbum(album.group)}
        >
          {busy ? "Queueing…" : "Queue"}
        </button>
      </div>
    </li>
  );
}

function ArtistPosterPlaceholder({ name }: { name: string }) {
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
