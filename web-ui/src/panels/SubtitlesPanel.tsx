import { FormEvent, useEffect, useState } from "react";
import {
  extractMediaSubtitles,
  getSubtitleExtractAll,
  getSubtitleScan,
  getSubtitleSettings,
  startSubtitleExtractAll,
  startSubtitleScan,
  updateSubtitleSettings,
  type ScannedEpisode,
  type ScannedShow,
  type SubtitleExtractStatus,
  type SubtitleScanStatus,
} from "../api";
import { usePolling } from "../hooks/usePolling";
import styles from "./SubtitlesPanel.module.css";

export function SubtitlesPanel() {
  const [scan, setScan] = useState<SubtitleScanStatus>({ status: "idle", scanned: 0, total: 0 });
  const [extract, setExtract] = useState<SubtitleExtractStatus>({
    status: "idle", processed: 0, total: 0, extracted: 0, skipped: 0, failed: 0,
  });
  const [expandedShows, setExpandedShows] = useState<Set<string>>(new Set());
  const [extractingMedia, setExtractingMedia] = useState<Set<string>>(new Set());
  const [mediaStatus, setMediaStatus] = useState<Map<string, string>>(new Map());

  // Language preference state (round-trips autoEnable so it isn't clobbered).
  const [langText, setLangText] = useState("");
  const [autoEnable, setAutoEnable] = useState(false);
  const [langLoaded, setLangLoaded] = useState(false);
  const [langBusy, setLangBusy] = useState(false);
  const [langStatus, setLangStatus] = useState("");

  // Initial load of job states and language preference.
  useEffect(() => {
    void getSubtitleScan().then(setScan).catch(() => {});
    void getSubtitleExtractAll().then(setExtract).catch(() => {});
    void getSubtitleSettings()
      .then((s) => {
        setLangText(s.subtitleLanguagePreference.join(", "));
        setAutoEnable(s.subtitleAutoEnable);
        setLangLoaded(true);
      })
      .catch(() => {});
  }, []);

  usePolling({
    enabled: scan.status === "running",
    intervalMs: 1500,
    maxIntervalMs: 15_000,
    immediate: false,
    task: async (signal) => {
      const next = await getSubtitleScan(signal);
      setScan(next);
      if (next.status !== "running") return "stop";
    },
  });

  usePolling({
    enabled: extract.status === "running",
    intervalMs: 2000,
    maxIntervalMs: 15_000,
    immediate: false,
    task: async (signal) => {
      const next = await getSubtitleExtractAll(signal);
      setExtract(next);
      if (next.status !== "running") return "stop";
    },
  });

  async function saveLang(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const parsed = parseSubtitleLanguageText(langText);
    if (!parsed.valid) { setLangStatus(parsed.message); return; }
    setLangBusy(true);
    setLangStatus("saving...");
    try {
      const saved = await updateSubtitleSettings({
        subtitleLanguagePreference: parsed.languages,
        subtitleAutoEnable: autoEnable,
      });
      setLangText(saved.subtitleLanguagePreference.join(", "));
      setAutoEnable(saved.subtitleAutoEnable);
      setLangStatus("saved");
    } catch (err) {
      setLangStatus(err instanceof Error ? err.message : String(err));
    } finally {
      setLangBusy(false);
    }
  }

  async function runScan() {
    try {
      const next = await startSubtitleScan();
      setScan(next);
    } catch (err) {
      setScan((prev) => ({ ...prev, status: "error", error: err instanceof Error ? err.message : String(err) }));
    }
  }

  async function runExtractAll() {
    try {
      const next = await startSubtitleExtractAll();
      setExtract(next);
    } catch (err) {
      setExtract((prev) => ({ ...prev, status: "error", error: err instanceof Error ? err.message : String(err) }));
    }
  }

  async function extractOne(mediaId: string, filename: string) {
    setExtractingMedia((prev) => new Set(prev).add(mediaId));
    setMediaStatus((prev) => new Map(prev).set(mediaId, "extracting…"));
    try {
      const result = await extractMediaSubtitles(mediaId);
      setMediaStatus((prev) => new Map(prev).set(mediaId, result.skipped ? "already extracted" : "extracted"));
    } catch (err) {
      setMediaStatus((prev) => new Map(prev).set(mediaId, err instanceof Error ? err.message : "error"));
    } finally {
      setExtractingMedia((prev) => {
        const next = new Set(prev);
        next.delete(mediaId);
        return next;
      });
    }
  }

  function toggleShow(name: string) {
    setExpandedShows((prev) => {
      const next = new Set(prev);
      if (next.has(name)) next.delete(name);
      else next.add(name);
      return next;
    });
  }

  const scanRunning = scan.status === "running";
  const extractRunning = extract.status === "running";
  const shows = scan.shows ?? [];
  const langs = activeLangs(langText);

  const showNeedsExtraction = (show: ScannedShow) =>
    show.seasons.some((s) =>
      s.episodes.some((ep) => episodeNeedsExtraction(ep, langs)),
    );

  const showIsAllBitmap = (show: ScannedShow) =>
    !showNeedsExtraction(show) &&
    show.seasons.some((s) => s.episodes.some((ep) => ep.tracks.length > 0)) &&
    !show.seasons.some((s) => s.episodes.some((ep) => episodeHasExtractableTracks(ep, langs)));

  const pendingShows = shows.filter(showNeedsExtraction);
  const bitmapShows = shows.filter(showIsAllBitmap);
  const doneShows = shows.filter((s) => !showNeedsExtraction(s) && !showIsAllBitmap(s));

  return (
    <div className="admin-panel">
      {/* Scan section */}
      <section className="admin-panel-section">
        <h2>Subtitles</h2>

        {/* Language preference */}
        <form className={styles["sub-lang-form"]} onSubmit={(e) => void saveLang(e)}>
          <label className={styles["sub-lang-label"]}>
            <span>languages</span>
            <input
              className={styles["sub-lang-input"]}
              value={langText}
              disabled={langBusy || !langLoaded}
              placeholder="eng, spa"
              onChange={(e) => { setLangText(e.target.value); setLangStatus(""); }}
            />
          </label>
          <button type="submit" disabled={langBusy || !langLoaded}>
            {langBusy ? "…" : "Save"}
          </button>
          {langStatus && <span className="muted">{langStatus}</span>}
        </form>

        {/* Scan / extract controls */}
        <div className="admin-form-actions" style={{ marginTop: "12px" }}>
          <button type="button" disabled={scanRunning} onClick={() => void runScan()}>
            {scanRunning ? "Scanning…" : scan.status === "done" ? "Re-scan library" : "Scan library"}
          </button>
          {scan.status === "done" && !extractRunning && (
            <button type="button" onClick={() => void runExtractAll()}>
              Extract all
            </button>
          )}
          {scanRunning && (
            <span className="muted">
              {scan.scanned} / {scan.total}
              {scan.total > 0 && (
                <> &nbsp;
                  <progress
                    className={styles["sub-scan-progress"]}
                    value={scan.scanned}
                    max={scan.total}
                  />
                </>
              )}
            </span>
          )}
          {scan.error && <span className="danger">{scan.error}</span>}
        </div>

        {/* Extract-all status */}
        {extract.status !== "idle" && (
          <div className={styles["sub-extract-status"]}>
            {extractRunning ? (
              <span className="muted">
                extracting… {extract.processed}/{extract.total}
                <progress className="sub-scan-progress" value={extract.processed} max={extract.total} />
              </span>
            ) : (
              <span className="muted">
                {extract.extracted} extracted
                {extract.skipped > 0 && ` · ${extract.skipped} already done`}
                {extract.failed > 0 && <span className="danger"> · {extract.failed} failed</span>}
              </span>
            )}
            {extract.error && <span className="danger">{extract.error}</span>}
          </div>
        )}
      </section>

      {/* Pending shows */}
      {pendingShows.length > 0 && (
        <section className="admin-panel-section">
          <h2>Needs extraction — {pendingShows.length} {pendingShows.length === 1 ? "show" : "shows"}</h2>
          <div className={styles["sub-show-list"]}>
            {pendingShows.map((show) => (
              <ShowRow
                key={show.name}
                show={show}
                expanded={expandedShows.has(show.name)}
                onToggle={() => toggleShow(show.name)}
                extractingMedia={extractingMedia}
                mediaStatus={mediaStatus}
                onExtract={extractOne}
                activeLangs={langs}
              />
            ))}
          </div>
        </section>
      )}

      {/* Bitmap-only shows */}
      {bitmapShows.length > 0 && (
        <section className="admin-panel-section">
          <h2>Unable to extract — {bitmapShows.length} {bitmapShows.length === 1 ? "show" : "shows"}</h2>
          <div className={styles["sub-show-list"]}>
            {bitmapShows.map((show) => (
              <ShowRow
                key={show.name}
                show={show}
                expanded={expandedShows.has(show.name)}
                onToggle={() => toggleShow(show.name)}
                extractingMedia={extractingMedia}
                mediaStatus={mediaStatus}
                onExtract={extractOne}
                activeLangs={langs}
              />
            ))}
          </div>
        </section>
      )}

      {/* Done shows */}
      {doneShows.length > 0 && (
        <section className="admin-panel-section">
          <h2>Extracted — {doneShows.length} {doneShows.length === 1 ? "show" : "shows"}</h2>
          <div className={styles["sub-show-list"]}>
            {doneShows.map((show) => (
              <ShowRow
                key={show.name}
                show={show}
                expanded={expandedShows.has(show.name)}
                onToggle={() => toggleShow(show.name)}
                extractingMedia={extractingMedia}
                mediaStatus={mediaStatus}
                onExtract={extractOne}
                activeLangs={langs}
              />
            ))}
          </div>
        </section>
      )}
    </div>
  );
}

function isEpisodeExtracted(ep: ScannedEpisode, langs: Set<string>): boolean {
  if (langs.size === 0) return ep.extractedLangs != null && ep.extractedLangs.length > 0;
  return Array.from(langs).every((l) => ep.extractedLangs?.includes(l));
}

function episodeHasExtractableTracks(ep: ScannedEpisode, langs: Set<string>): boolean {
  const relevant = langs.size > 0 ? ep.tracks.filter((t) => langs.has(t.language)) : ep.tracks;
  return relevant.some((t) => !t.isBitmap);
}

function episodeNeedsExtraction(ep: ScannedEpisode, langs: Set<string>): boolean {
  return episodeHasExtractableTracks(ep, langs) && !isEpisodeExtracted(ep, langs);
}

function ShowRow({
  show,
  expanded,
  onToggle,
  extractingMedia,
  mediaStatus,
  onExtract,
  activeLangs,
}: {
  show: ScannedShow;
  expanded: boolean;
  onToggle: () => void;
  extractingMedia: Set<string>;
  mediaStatus: Map<string, string>;
  onExtract: (mediaId: string, filename: string) => void;
  activeLangs: Set<string>;
}) {
  const extractableEps = show.seasons.reduce(
    (n, s) => n + s.episodes.filter((ep) => episodeHasExtractableTracks(ep, activeLangs)).length,
    0,
  );
  const extractedEps = show.seasons.reduce(
    (n, s) => n + s.episodes.filter((ep) => episodeHasExtractableTracks(ep, activeLangs) && isEpisodeExtracted(ep, activeLangs)).length,
    0,
  );

  return (
    <div className={styles["sub-show"]}>
      <button type="button" className={styles["sub-show-header"]} onClick={onToggle}>
        <span className={styles["sub-show-chevron"]}>{expanded ? "▼" : "▶"}</span>
        <span className={styles["sub-show-name"]}>{show.name}</span>
        <span className={`muted ${styles["sub-show-meta"]}`}>
          {extractableEps === 0 ? "image-based only" : `${extractedEps} / ${extractableEps} ep`}
        </span>
      </button>

      {expanded && (
        <div className={styles["sub-seasons"]}>
          {show.seasons.map((season) => (
            <SeasonBlock
              key={season.name || "__root__"}
              season={season}
              extractingMedia={extractingMedia}
              mediaStatus={mediaStatus}
              onExtract={onExtract}
              activeLangs={activeLangs}
            />
          ))}
        </div>
      )}
    </div>
  );
}

function SeasonBlock({
  season,
  extractingMedia,
  mediaStatus,
  onExtract,
  activeLangs,
}: {
  season: { name: string; episodes: ScannedEpisode[] };
  extractingMedia: Set<string>;
  mediaStatus: Map<string, string>;
  onExtract: (mediaId: string, filename: string) => void;
  activeLangs: Set<string>;
}) {
  return (
    <div className={styles["sub-season"]}>
      {season.name && <div className={styles["sub-season-name"]}>{season.name}</div>}
      {season.episodes.map((ep) => (
        <EpisodeRow
          key={ep.mediaId}
          ep={ep}
          extracting={extractingMedia.has(ep.mediaId)}
          status={mediaStatus.get(ep.mediaId)}
          onExtract={() => onExtract(ep.mediaId, ep.filename)}
          activeLangs={activeLangs}
        />
      ))}
    </div>
  );
}

function EpisodeRow({
  ep,
  extracting,
  status,
  onExtract,
  activeLangs,
}: {
  ep: ScannedEpisode;
  extracting: boolean;
  status?: string;
  onExtract: () => void;
  activeLangs: Set<string>;
}) {
  const tracks = activeLangs.size > 0 ? ep.tracks.filter((t) => activeLangs.has(t.language)) : ep.tracks;
  const hasText = tracks.some((t) => !t.isBitmap);

  return (
    <div className={styles["sub-episode"]}>
      <div className={styles["sub-episode-header"]}>
        <span className={styles["sub-episode-name"]}>{ep.filename}</span>
        {ep.packaged && hasText && (
          <button
            type="button"
            className={styles["sub-extract-btn"]}
            disabled={extracting}
            onClick={onExtract}
          >
            {extracting ? "…" : "Extract"}
          </button>
        )}
        {status && <span className={`muted ${styles["sub-episode-status"]}`}>{status}</span>}
      </div>

      {tracks.length === 0 ? (
        <span className={`muted ${styles["sub-no-tracks"]}`}>no subtitle tracks</span>
      ) : (
        <ul className={styles["sub-track-list"]}>
          {tracks.map((track) => (
            <li key={track.index} className={`${styles["sub-track-row"]}${track.isBitmap ? " is-bitmap" : ""}`}>
              <span className={styles["sub-track-lang"]}>{track.language}</span>
              <span className={styles["sub-track-codec"]}>{track.codec}</span>
              {track.title && <span className={styles["sub-track-title"]}>{track.title}</span>}
              {track.isBitmap && (
                <span className={styles["sub-track-unsupported"]}>image-based — extraction not supported</span>
              )}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

function activeLangs(langText: string): Set<string> {
  const result = parseSubtitleLanguageText(langText);
  return result.valid ? new Set(result.languages) : new Set();
}

function parseSubtitleLanguageText(text: string):
  | { valid: true; languages: string[] }
  | { valid: false; message: string } {
  const languages = text
    .split(/[,\s]+/)
    .map((s) => s.trim().toLowerCase())
    .filter(Boolean);
  const unique = Array.from(new Set(languages));
  if (unique.length === 0) return { valid: false, message: "enter at least one language code" };
  const invalid = unique.find((l) => !/^[a-z]{3}$/.test(l));
  if (invalid) return { valid: false, message: `${invalid} must be a 3-letter ISO 639-2 code` };
  return { valid: true, languages: unique };
}
