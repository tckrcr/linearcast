import { useEffect, useState } from "react";
import {
  addChannelMedia as apiAddChannelMedia,
  extendChannel as apiExtendChannel,
  getChannelMedia,
  moveChannelMedia as apiMoveChannelMedia,
  removeChannelMedia as apiRemoveChannelMedia,
  searchMedia as apiSearchMedia,
  type MediaSearchResult,
} from "../api";
import type { ChannelMediaList } from "../types";
import styles from "./ChannelMediaPanel.module.css";

export function ChannelMediaPanel({ channelId }: { channelId: string }) {
  const [open, setOpen] = useState(false);
  const [data, setData] = useState<ChannelMediaList | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [addQuery, setAddQuery] = useState("");
  const [addResults, setAddResults] = useState<MediaSearchResult[]>([]);
  const [addDropdownOpen, setAddDropdownOpen] = useState(false);
  const [selectedMedia, setSelectedMedia] = useState<MediaSearchResult | null>(null);
  const [addBusy, setAddBusy] = useState(false);
  const [removeBusy, setRemoveBusy] = useState<Record<string, boolean>>({});
  const [reorderMode, setReorderMode] = useState(false);
  const [dragIdx, setDragIdx] = useState<number | null>(null);
  const [moveBusy, setMoveBusy] = useState(false);
  const [rebuildBusy, setRebuildBusy] = useState(false);

  useEffect(() => {
    setOpen(false);
    setData(null);
    setError("");
    setNotice("");
    setAddQuery("");
    setAddResults([]);
    setAddDropdownOpen(false);
    setSelectedMedia(null);
    setAddBusy(false);
    setRemoveBusy({});
    setReorderMode(false);
    setDragIdx(null);
    setMoveBusy(false);
    setRebuildBusy(false);
  }, [channelId]);

  async function loadData(silent = false) {
    if (!silent) setLoading(true);
    setError("");
    try {
      const d = await getChannelMedia(channelId);
      setData(d);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      if (!silent) setLoading(false);
    }
  }

  function toggleOpen() {
    const next = !open;
    setOpen(next);
    if (next && !data) void loadData();
  }

  useEffect(() => {
    const q = addQuery.trim();
    if (!q || selectedMedia) return;
    const timer = window.setTimeout(async () => {
      try {
        const results = await apiSearchMedia(q, channelId);
        setAddResults(results);
        setAddDropdownOpen(results.length > 0);
      } catch {
        // tolerate search failures silently
      }
    }, 300);
    return () => window.clearTimeout(timer);
  }, [addQuery, channelId, selectedMedia]);

  async function handleAdd(e: React.FormEvent) {
    e.preventDefault();
    const mediaId = selectedMedia ? selectedMedia.mediaId : addQuery.trim();
    if (!mediaId || addBusy) return;
    setAddBusy(true);
    setError("");
    setNotice("");
    try {
      await apiAddChannelMedia(channelId, mediaId);
      setAddQuery("");
      setAddResults([]);
      setAddDropdownOpen(false);
      setSelectedMedia(null);
      const label = selectedMedia?.title || mediaId;
      setNotice(`added ${label}`);
      await loadData(true);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setAddBusy(false);
    }
  }

  async function handleRemove(media: ChannelMediaList["media"][number]) {
    const label = media.title || media.mediaId;
    if (
      !window.confirm(
        `Remove "${label}" from this channel?\n\nFuture schedule entries for this media will be pruned and the schedule rebuilt from that point.`,
      )
    )
      return;
    setRemoveBusy((prev) => ({ ...prev, [media.mediaId]: true }));
    setError("");
    setNotice("");
    try {
      const res = await apiRemoveChannelMedia(channelId, media.mediaId);
      setNotice(
        `removed ${label}${res.prunedSchedule > 0 ? `; pruned ${res.prunedSchedule} schedule entries` : ""}`,
      );
      await loadData(true);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setRemoveBusy((prev) => ({ ...prev, [media.mediaId]: false }));
    }
  }

  function beginReorder() {
    if (!data) return;
    setReorderMode(true);
    setError("");
    setNotice("");
  }

  function endReorder() {
    setReorderMode(false);
    setDragIdx(null);
  }

  // moveItem reorders a single media item in the channel's linked-list chain
  // by calling the move endpoint. Optimistically updates local state so the
  // UI reorders immediately; on error, reloads from the server.
  async function moveItem(from: number, to: number) {
    if (!data || moveBusy) return;
    if (from === to || from < 0 || to < 0 || from >= data.media.length || to >= data.media.length) return;

    const current = data.media;
    const moving = current[from];
    const reordered = [...current];
    reordered.splice(from, 1);
    reordered.splice(to, 0, moving);
    // afterMediaId is the item that ends up immediately before the moved item
    // in the new order, or "" if the moved item is now the head.
    const afterMediaId = to === 0 ? "" : reordered[to - 1].mediaId;

    setMoveBusy(true);
    setError("");
    setNotice("");
    setData({ ...data, media: reordered });
    try {
      await apiMoveChannelMedia(channelId, moving.mediaId, afterMediaId);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      await loadData(true);
    } finally {
      setMoveBusy(false);
    }
  }

  async function rebuildTail() {
    if (rebuildBusy) return;
    setRebuildBusy(true);
    setError("");
    setNotice("");
    try {
      const res = await apiExtendChannel(channelId);
      setNotice(
        res.note
          ? res.note
          : res.skippedLowWater
            ? "schedule already at coverage target"
            : `rebuilt — inserted ${res.inserted} entries`,
      );
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setRebuildBusy(false);
    }
  }

  const anyRemoveBusy = Object.values(removeBusy).some(Boolean);
  const mutationBusy = addBusy || moveBusy || anyRemoveBusy || rebuildBusy;
  const displayList = data?.media ?? [];

  return (
    <section className="admin-panel-section">
      <div className="section-headline">
        <h3>
          Episodes
          {data != null && <span className="muted"> ({data.count})</span>}
        </h3>
        <button type="button" onClick={toggleOpen}>
          {open ? "Hide" : "Show"}
        </button>
      </div>

      {open && (
        <>
          {loading && <p className="muted">loading…</p>}
          {error && <p className="error">{error}</p>}
          {notice && <p className="success">{notice}</p>}

          {data && !loading && (
            <>
              <div className={styles["episode-edit-bar"]}>
                {!reorderMode ? (
                  <>
                    <button
                      type="button"
                      disabled={data.count === 0 || mutationBusy}
                      onClick={beginReorder}
                    >
                      Reorder
                    </button>
                    <button
                      type="button"
                      className="primary"
                      disabled={mutationBusy}
                      title="Extend the schedule tail using the current episode order"
                      onClick={() => void rebuildTail()}
                    >
                      {rebuildBusy ? "Rebuilding…" : "Rebuild tail"}
                    </button>
                    <button type="button" disabled={mutationBusy} onClick={() => void loadData()}>
                      Refresh
                    </button>
                  </>
                ) : (
                  <>
                    <button
                      type="button"
                      className="primary"
                      disabled={moveBusy}
                      onClick={endReorder}
                    >
                      Done
                    </button>
                    <span className="muted">
                      {moveBusy ? "moving…" : "drag or use ↑↓ — each move saves automatically"}
                    </span>
                  </>
                )}
              </div>

              {!reorderMode && (
                <form className={styles["episode-add-form"]} onSubmit={(e) => void handleAdd(e)}>
                  <div className={styles["episode-typeahead"]}>
                    <input
                      value={addQuery}
                      placeholder="search title, path, or group…"
                      disabled={addBusy}
                      autoComplete="off"
                      onChange={(e) => {
                        const val = e.target.value;
                        setAddQuery(val);
                        if (selectedMedia && val !== (selectedMedia.title || selectedMedia.mediaId)) {
                          setSelectedMedia(null);
                        }
                        if (!val.trim()) {
                          setAddResults([]);
                          setAddDropdownOpen(false);
                        }
                      }}
                      onFocus={() => { if (addResults.length > 0) setAddDropdownOpen(true); }}
                      onBlur={() => window.setTimeout(() => setAddDropdownOpen(false), 150)}
                    />
                    {addDropdownOpen && (
                      <ul className={styles["episode-typeahead-dropdown"]}>
                        {addResults.map((r) => (
                          <li
                            key={r.mediaId}
                            className={`${styles["episode-typeahead-result"]}${!r.codecCheckPassed ? " is-ineligible" : ""}`}
                            onMouseDown={(e) => {
                              e.preventDefault();
                              setSelectedMedia(r);
                              setAddQuery(r.title || r.mediaId);
                              setAddDropdownOpen(false);
                            }}
                          >
                            <span className={styles["episode-typeahead-title"]}>{r.title || r.mediaId}</span>
                            {r.schedulingGroup && (
                              <span className={styles["episode-typeahead-group"]}>{r.schedulingGroup}</span>
                            )}
                            {!r.codecCheckPassed && (
                              <span className={styles["episode-typeahead-ineligible"]}>codec check failed</span>
                            )}
                          </li>
                        ))}
                      </ul>
                    )}
                  </div>
                  <button
                    type="submit"
                    disabled={!(selectedMedia ? selectedMedia.mediaId : addQuery.trim()) || addBusy || (selectedMedia != null && !selectedMedia.codecCheckPassed)}
                  >
                    {addBusy ? "Adding…" : "Add"}
                  </button>
                </form>
              )}

              {displayList.length === 0 && <p className="muted">no episodes</p>}
              {displayList.length > 0 && (
                <ul className={styles["episode-list"]}>
                  {displayList.map((media, idx) => {
                    const label = media.title || media.mediaId;
                    const isRemoving = removeBusy[media.mediaId] ?? false;
                    const pkgClass = packageStatusClass(media.packageStatus, media.packageReady);
                    return (
                      <li
                        key={media.mediaId}
                        className={`${styles["episode-entry"]}${reorderMode ? " is-editing" : ""}`}
                        draggable={reorderMode && !moveBusy}
                        onDragStart={() => setDragIdx(idx)}
                        onDragOver={(e) => { if (reorderMode) e.preventDefault(); }}
                        onDrop={(e) => {
                          e.preventDefault();
                          const from = dragIdx;
                          setDragIdx(null);
                          if (from != null && from !== idx) void moveItem(from, idx);
                        }}
                        onDragEnd={() => setDragIdx(null)}
                      >
                        {reorderMode && (
                          <span className={styles["episode-drag-handle"]} title="Drag to reorder">↕</span>
                        )}
                        <span className={styles["episode-title"]} title={media.path}>{label}</span>
                        {media.schedulingGroup && (
                          <span className={`${styles["episode-group"]} muted`}>{media.schedulingGroup}</span>
                        )}
                        <span className={`episode-pkg ${pkgClass}`}>{media.packageStatus}</span>
                        {reorderMode ? (
                          <div className={styles["episode-move"]}>
                            <button
                              type="button"
                              disabled={idx === 0 || moveBusy}
                              onClick={() => void moveItem(idx, idx - 1)}
                            >↑</button>
                            <button
                              type="button"
                              disabled={idx === displayList.length - 1 || moveBusy}
                              onClick={() => void moveItem(idx, idx + 1)}
                            >↓</button>
                          </div>
                        ) : (
                          <button
                            type="button"
                            className={`${styles["episode-remove"]} danger`}
                            disabled={isRemoving || mutationBusy}
                            title="Remove from channel"
                            onClick={() => void handleRemove(media)}
                          >
                            {isRemoving ? "…" : "Remove"}
                          </button>
                        )}
                      </li>
                    );
                  })}
                </ul>
              )}
            </>
          )}
        </>
      )}
    </section>
  );
}

function packageStatusClass(status: string, ready: boolean): string {
  if (ready) return "episode-pkg-ready";
  if (status === "failed") return "episode-pkg-failed";
  if (status === "missing") return "episode-pkg-missing";
  return "episode-pkg-pending";
}
