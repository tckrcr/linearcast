import { useCallback, useEffect, useMemo, useState } from "react";
import {
  bulkUpdateMediaCollections,
  deleteMedia,
  getMediaGroups,
  getMediaInventory,
  updateMediaFields,
  type MediaCollectionBulkAction,
  type MediaInventoryItem,
} from "../api";
import { formatMs } from "../format";
import styles from "./InventoryPanel.module.css";

const PAGE_SIZE = 100;
type InventoryKind = "" | "shows" | "movies" | "music" | "filler";
type BulkTarget = "selected" | "matching";
type BulkMode = "set-clear" | "rename";
type SortField = "title" | "episode" | "collection" | "duration" | "packages";
type SortDir = "asc" | "desc";

function packageLabel(item: MediaInventoryItem) {
  const parts = [
    item.readyPackages > 0 ? `${item.readyPackages} ready` : "",
    item.pendingPackages > 0 ? `${item.pendingPackages} queued` : "",
    item.processingPackages > 0 ? `${item.processingPackages} encoding` : "",
    item.failedPackages > 0 ? `${item.failedPackages} failed` : "",
  ].filter(Boolean);
  return parts.length > 0 ? parts.join(" · ") : "missing";
}

function visibleCollectionName(value: string) {
  return value.startsWith("movie:") ? value.slice("movie:".length) : value;
}

function isMovieCollection(value: string) {
  return value.startsWith("movie:");
}

function episodeOrderLabel(item: MediaInventoryItem) {
  if (item.seasonNumber != null && item.episodeNumber != null) {
    return `S${String(item.seasonNumber).padStart(2, "0")}E${String(item.episodeNumber).padStart(2, "0")}`;
  }
  return item.episodeCode || "—";
}

function parseOrderingDraft(label: string, value: string): number | null {
  const trimmed = value.trim();
  if (!trimmed) return null;
  const n = Number(trimmed);
  if (!Number.isInteger(n) || n < 1) {
    throw new Error(`${label} must be blank or a positive integer`);
  }
  return n;
}

function resolutionLabel(item: MediaInventoryItem) {
  const width = item.videoWidth ?? 0;
  const height = item.videoHeight ?? 0;
  if (width >= 3840) return "2160p";
  if (width >= 1920) return "1080p";
  if (width >= 1280) return "720p";
  if (width > 0 || height > 0) return "480p";
  return "—";
}

export function InventoryPanel() {
  const [query, setQuery] = useState("");
  const [debouncedQuery, setDebouncedQuery] = useState("");
  const [kind, setKind] = useState<InventoryKind>("");
  const [collection, setCollection] = useState("");
  const [showSuggestionsOpen, setShowSuggestionsOpen] = useState(false);
  const [packageStatus, setPackageStatus] = useState("");
  const [titleFilter, setTitleFilter] = useState("");
  const [episodeFilter, setEpisodeFilter] = useState("");
  const [mediaFilter, setMediaFilter] = useState("");
  const [sourceFilter, setSourceFilter] = useState("");
  const [sortBy, setSortBy] = useState<SortField>("title");
  const [sortDir, setSortDir] = useState<SortDir>("asc");
  const [collections, setCollections] = useState<string[]>([]);
  const [rows, setRows] = useState<MediaInventoryItem[]>([]);
  const [count, setCount] = useState(0);
  const [offset, setOffset] = useState(0);
  const [loading, setLoading] = useState(false);
  const [loadingMore, setLoadingMore] = useState(false);
  const [status, setStatus] = useState("");
  const [selectedIds, setSelectedIds] = useState<Set<string>>(new Set());
  const [editingId, setEditingId] = useState<string | null>(null);
  const [draftTitle, setDraftTitle] = useState("");
  const [draftCollection, setDraftCollection] = useState("");
  const [draftSeason, setDraftSeason] = useState("");
  const [draftEpisode, setDraftEpisode] = useState("");
  const [savingId, setSavingId] = useState<string | null>(null);
  const [deletingId, setDeletingId] = useState<string | null>(null);
  const [bulkCollection, setBulkCollection] = useState("");
  const [bulkFromCollection, setBulkFromCollection] = useState("");
  const [bulkMode, setBulkMode] = useState<BulkMode>("set-clear");
  const [bulkTarget, setBulkTarget] = useState<BulkTarget>("selected");
  const [bulkBusy, setBulkBusy] = useState(false);

  useEffect(() => {
    const id = window.setTimeout(() => setDebouncedQuery(query.trim()), 250);
    return () => window.clearTimeout(id);
  }, [query]);

  const filters = useMemo(() => ({
    q: debouncedQuery || undefined,
    title: titleFilter.trim() || undefined,
    episode: episodeFilter.trim() || undefined,
    media: mediaFilter.trim() || undefined,
    source: sourceFilter || undefined,
    kind: kind || undefined,
    collection: collection || undefined,
    codecStatus: "passed" as const,
    packageStatus: packageStatus || undefined,
    sortBy,
    sortDir,
  }), [collection, debouncedQuery, episodeFilter, kind, mediaFilter, packageStatus, sortBy, sortDir, sourceFilter, titleFilter]);

  const load = useCallback(async () => {
    setLoading(true);
    setStatus("");
    try {
      const next = await getMediaInventory({ ...filters, limit: PAGE_SIZE, offset: 0 });
      setRows(next.media);
      setCount(next.count);
      setOffset(0);
      setSelectedIds(new Set());
    } catch (err) {
      setStatus(err instanceof Error ? err.message : String(err));
    } finally {
      setLoading(false);
    }
  }, [filters]);

  useEffect(() => {
    void load();
  }, [load]);

  useEffect(() => {
    getMediaGroups()
      .then(setCollections)
      .catch(() => {});
  }, []);

  async function loadMore() {
    const nextOffset = offset + PAGE_SIZE;
    setLoadingMore(true);
    setStatus("");
    try {
      const next = await getMediaInventory({ ...filters, limit: PAGE_SIZE, offset: nextOffset });
      setRows((prev) => {
        const seen = new Set(prev.map((item) => item.mediaId));
        return [...prev, ...next.media.filter((item) => !seen.has(item.mediaId))];
      });
      setCount(next.count);
      setOffset(nextOffset);
    } catch (err) {
      setStatus(err instanceof Error ? err.message : String(err));
    } finally {
      setLoadingMore(false);
    }
  }

  function startEdit(item: MediaInventoryItem) {
    setEditingId(item.mediaId);
    setDraftTitle(item.title);
    setDraftCollection(item.collection);
    setDraftSeason(item.seasonNumber != null ? String(item.seasonNumber) : "");
    setDraftEpisode(item.episodeNumber != null ? String(item.episodeNumber) : "");
  }

  async function saveEdit(item: MediaInventoryItem) {
    let seasonNumber: number | null;
    let episodeNumber: number | null;
    try {
      seasonNumber = parseOrderingDraft("season", draftSeason);
      episodeNumber = parseOrderingDraft("episode", draftEpisode);
    } catch (err) {
      setStatus(err instanceof Error ? err.message : String(err));
      return;
    }
    setSavingId(item.mediaId);
    setStatus("");
    try {
      const res = await updateMediaFields(item.mediaId, {
        title: draftTitle,
        collectionName: draftCollection.trim() || undefined,
        seasonNumber,
        episodeNumber,
      });
      setRows((prev) => prev.map((row) => row.mediaId === item.mediaId
        ? {
          ...row,
          title: res.title,
          collection: res.collectionName,
          seasonNumber: res.seasonNumber,
          episodeNumber: res.episodeNumber,
        }
        : row));
      setEditingId(null);
    } catch (err) {
      setStatus(err instanceof Error ? err.message : String(err));
    } finally {
      setSavingId(null);
    }
  }

  async function deleteRow(item: MediaInventoryItem) {
    if (!window.confirm(`Delete "${item.title || item.mediaId}" from the inventory?\n\nThis removes the media row and packaged metadata from Linearcast.`)) {
      return;
    }
    setDeletingId(item.mediaId);
    setStatus("");
    try {
      const res = await deleteMedia(item.mediaId);
      if (!res.deleted) {
        const blockers = res.blockers?.map((blocker) => `${blocker.displayName || blocker.channelId} (${blocker.kind})`).join(", ");
        setStatus(blockers ? `delete blocked by ${blockers}` : "delete blocked");
        return;
      }
      setRows((prev) => prev.filter((row) => row.mediaId !== item.mediaId));
      setCount((prev) => Math.max(0, prev - 1));
      setSelectedIds((prev) => {
        const next = new Set(prev);
        next.delete(item.mediaId);
        return next;
      });
      setStatus(res.warnings && res.warnings.length > 0 ? `deleted with warnings: ${res.warnings.join(" · ")}` : "metadata deleted");
    } catch (err) {
      setStatus(err instanceof Error ? err.message : String(err));
    } finally {
      setDeletingId(null);
    }
  }

  function toggleSelected(mediaId: string, checked: boolean) {
    setSelectedIds((prev) => {
      const next = new Set(prev);
      if (checked) next.add(mediaId);
      else next.delete(mediaId);
      return next;
    });
  }

  function toggleVisible(checked: boolean) {
    setSelectedIds(checked ? new Set(rows.map((item) => item.mediaId)) : new Set());
  }

  function hasActiveFilter() {
    return Boolean(
      filters.q || filters.title || filters.episode || filters.media ||
      filters.kind || filters.collection || filters.packageStatus || filters.source,
    );
  }

  function currentFilteredCollectionName() {
    if (!collection) return "";
    return visibleCollectionName(collection);
  }

  function bulkScopeRequest(usingSelected: boolean, selected: MediaInventoryItem[]) {
    return {
      mediaIds: usingSelected ? selected.map((item) => item.mediaId) : undefined,
      filter: usingSelected ? undefined : filters,
    };
  }

  async function runBulkMutation(input: {
    action: MediaCollectionBulkAction;
    collection?: string;
    fromCollection?: string;
    statusVerb: string;
    confirmVerb: string;
  }) {
    const selected = rows.filter((row) => selectedIds.has(row.mediaId));
    const usingSelected = bulkTarget === "selected";
    if (usingSelected && selected.length === 0) return;
    if (!usingSelected && !hasActiveFilter()) {
      setStatus("choose at least one filter before applying to all matching rows");
      return;
    }

    const request = {
      action: input.action,
      collection: input.collection || undefined,
      fromCollection: input.fromCollection || undefined,
      ...bulkScopeRequest(usingSelected, selected),
    };

    setBulkBusy(true);
    setStatus("previewing bulk update...");
    try {
      const preview = await bulkUpdateMediaCollections({ ...request, dryRun: true });
      if (preview.matched === 0) {
        setStatus("bulk update matched 0 rows");
        return;
      }
      const targetLabel = usingSelected ? "selected" : "matching filtered";
      if (!window.confirm(`${input.confirmVerb} for ${preview.matched} ${targetLabel} media row(s)?`)) {
        setStatus("bulk update cancelled");
        return;
      }
      setStatus(`${input.statusVerb} ${preview.matched} row(s)...`);
      const result = await bulkUpdateMediaCollections({ ...request, dryRun: false });
      setSelectedIds(new Set());
      setBulkCollection("");
      setBulkFromCollection("");
      setStatus(`updated ${result.updated} row(s)`);
      await load();
      getMediaGroups().then(setCollections).catch(() => {});
    } catch (err) {
      setStatus(err instanceof Error ? err.message : String(err));
    } finally {
      setBulkBusy(false);
    }
  }

  async function bulkUpdateCollection(action: Extract<MediaCollectionBulkAction, "set" | "clear">) {
    const nextCollection = action === "set" ? bulkCollection.trim() : "";
    if (action === "set" && !nextCollection) return;
    await runBulkMutation({
      action,
      collection: nextCollection,
      statusVerb: "updating",
      confirmVerb: action === "clear" ? "Clear show" : `Set show to "${nextCollection}"`,
    });
  }

  async function bulkRenameCollection() {
    const fromCollection = (bulkFromCollection.trim() || currentFilteredCollectionName()).trim();
    const nextCollection = bulkCollection.trim();
    if (!fromCollection || !nextCollection) return;
    await runBulkMutation({
      action: "rename",
      fromCollection,
      collection: nextCollection,
      statusVerb: "renaming",
      confirmVerb: `Rename show "${fromCollection}" to "${nextCollection}"`,
    });
  }

  function toggleSort(field: SortField) {
    if (sortBy === field) {
      setSortDir((prev) => (prev === "asc" ? "desc" : "asc"));
      return;
    }
    setSortBy(field);
    setSortDir("asc");
  }

  const allVisibleSelected = rows.length > 0 && rows.every((item) => selectedIds.has(item.mediaId));
  const selectedCount = selectedIds.size;
  const loadedLabel = loading ? "loading" : `${rows.length}/${count} loaded`;
  const bulkCanTargetMatching = hasActiveFilter();
  const bulkTargetValid = bulkTarget === "selected" ? selectedCount > 0 : bulkCanTargetMatching;
  const renameFromValue = bulkFromCollection || currentFilteredCollectionName();
  const showSuggestions = useMemo(() => {
    const q = collection.trim().toLowerCase();
    return collections
      .filter((item) => !isMovieCollection(item))
      .map(visibleCollectionName)
      .filter((item, index, items) => item && items.indexOf(item) === index)
      .filter((item) => !q || item.toLowerCase().includes(q))
      .slice(0, 12);
  }, [collection, collections]);

  return (
    <div className={`admin-panel ${styles["inventory-panel"]}`}>
      <section className="admin-panel-section">
        <div className="section-headline">
          <div className="section-headline-main">
            <h2>Inventory</h2>
            <p className="section-purpose">
              Everything scanned from your media sources, including filler assets. Search across
              titles, paths, shows, and sources.
            </p>
          </div>
          <button type="button" disabled={loading} onClick={() => void load()}>
            {loading ? "refreshing" : "refresh"}
          </button>
        </div>

        <div className={styles["inventory-toolbar"]}>
          <label>
            <span>search</span>
            <input
              value={query}
              placeholder="title, path, show, source"
              onChange={(event) => setQuery(event.target.value)}
            />
          </label>
          <label>
            <span>kind</span>
            <select value={kind} onChange={(event) => setKind(event.target.value as InventoryKind)}>
              <option value="">all</option>
              <option value="shows">tv shows</option>
              <option value="movies">movies</option>
              <option value="music">music</option>
              <option value="filler">filler</option>
            </select>
          </label>
          <label>
            <span>packages</span>
            <select value={packageStatus} onChange={(event) => setPackageStatus(event.target.value)}>
              <option value="">all</option>
              <option value="missing">missing</option>
              <option value="ready">ready</option>
              <option value="pending">queued</option>
              <option value="processing">encoding</option>
              <option value="failed">failed</option>
            </select>
          </label>
          <label>
            <span>source</span>
            <select value={sourceFilter} onChange={(event) => setSourceFilter(event.target.value)}>
              <option value="">all</option>
              <option value="local">local</option>
              <option value="plex">plex</option>
              <option value="jellyfin">jellyfin</option>
              <option value="external">external</option>
            </select>
          </label>
          <button type="button" disabled={loading} onClick={() => {
            setQuery("");
            setKind("");
            setCollection("");
            setShowSuggestionsOpen(false);
            setPackageStatus("");
            setTitleFilter("");
            setEpisodeFilter("");
            setMediaFilter("");
            setSourceFilter("");
            setSortBy("title");
            setSortDir("asc");
          }}>
            reset
          </button>
        </div>

        <div className={styles["inventory-summary"]}>
          <label>
            <input
              type="checkbox"
              checked={allVisibleSelected}
              disabled={rows.length === 0}
              onChange={(event) => toggleVisible(event.target.checked)}
            />
            <span> select visible</span>
          </label>
          <span className="muted">{loadedLabel}</span>
          <span className="muted">sorted by {sortBy} {sortDir}</span>
          {selectedCount > 0 && <span className="muted">{selectedCount} selected</span>}
          {status && <span className="muted">{status}</span>}
        </div>

        {(selectedCount > 0 || bulkCanTargetMatching) && (
          <div className={styles["bulk-bar"]}>
            <label>
              <span>action</span>
              <select value={bulkMode} onChange={(event) => setBulkMode(event.target.value as BulkMode)} disabled={bulkBusy}>
                <option value="set-clear">set / clear</option>
                <option value="rename">rename</option>
              </select>
            </label>
            <label>
              <span>target</span>
              <select value={bulkTarget} onChange={(event) => setBulkTarget(event.target.value as BulkTarget)} disabled={bulkBusy}>
                <option value="selected" disabled={selectedCount === 0}>{selectedCount} selected</option>
                <option value="matching" disabled={!bulkCanTargetMatching}>all {count} matching filter</option>
              </select>
            </label>
            {bulkMode === "rename" && (
              <label className={styles["bulk-input"]}>
                <span>from show</span>
                <input
                  value={renameFromValue}
                  placeholder="old show"
                  onChange={(event) => setBulkFromCollection(event.target.value)}
                  disabled={bulkBusy}
                />
              </label>
            )}
            <label className={styles["bulk-input"]}>
              <span>{bulkMode === "rename" ? "to show" : "bulk show"}</span>
              <input
                value={bulkCollection}
                placeholder="show name"
                onChange={(event) => setBulkCollection(event.target.value)}
                disabled={bulkBusy}
              />
            </label>
            {bulkMode === "rename" ? (
              <button
                type="button"
                disabled={bulkBusy || !renameFromValue.trim() || !bulkCollection.trim() || !bulkTargetValid}
                onClick={() => void bulkRenameCollection()}
              >
                Rename show
              </button>
            ) : (
              <>
                <button type="button" disabled={bulkBusy || !bulkCollection.trim() || !bulkTargetValid} onClick={() => void bulkUpdateCollection("set")}>
                  Set show
                </button>
                <button type="button" disabled={bulkBusy || !bulkTargetValid} onClick={() => void bulkUpdateCollection("clear")}>
                  Clear show
                </button>
              </>
            )}
          </div>
        )}

        <div className={styles["table-wrap"]}>
          <table className={styles["inventory-table"]}>
            <thead>
              <tr>
                <th className={styles["select-col"]} />
                <th className={styles["title-col"]}>
                  <button type="button" className={styles["sort-button"]} onClick={() => toggleSort("title")}>title</button>
                  <input value={titleFilter} placeholder="filter" onChange={(event) => setTitleFilter(event.target.value)} />
                </th>
                <th className={styles["episode-col"]}>
                  <button type="button" className={styles["sort-button"]} onClick={() => toggleSort("episode")}>episode</button>
                  <input value={episodeFilter} placeholder="S01E01" onChange={(event) => setEpisodeFilter(event.target.value)} />
                </th>
                <th className={styles["collection-col"]}>
                  <button type="button" className={styles["sort-button"]} onClick={() => toggleSort("collection")}>show</button>
                  <div className={styles["show-filter"]}>
                    <input
                      value={collection}
                      placeholder="filter"
                      onBlur={() => window.setTimeout(() => setShowSuggestionsOpen(false), 120)}
                      onChange={(event) => {
                        setCollection(event.target.value);
                        setShowSuggestionsOpen(true);
                      }}
                      onFocus={() => setShowSuggestionsOpen(true)}
                    />
                    {showSuggestionsOpen && showSuggestions.length > 0 && (
                      <div className={styles["show-suggestions"]}>
                        {showSuggestions.map((item) => (
                          <button
                            key={item}
                            type="button"
                            onMouseDown={(event) => event.preventDefault()}
                            onClick={() => {
                              setCollection(item);
                              setShowSuggestionsOpen(false);
                            }}
                          >
                            {item}
                          </button>
                        ))}
                      </div>
                    )}
                  </div>
                </th>
                <th className={styles["duration-col"]}>
                  <button type="button" className={styles["sort-button"]} onClick={() => toggleSort("duration")}>duration</button>
                  <input value={mediaFilter} placeholder="hevc, 2160, truehd" onChange={(event) => setMediaFilter(event.target.value)} />
                </th>
                <th className={styles["resolution-col"]}>resolution</th>
                <th className={styles["codec-col"]}>video</th>
                <th className={styles["codec-col"]}>audio</th>
                <th className={styles["package-col"]}>
                  <button type="button" className={styles["sort-button"]} onClick={() => toggleSort("packages")}>packages</button>
                </th>
                <th className={styles["actions-col"]}>actions</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((item) => {
                const editing = editingId === item.mediaId;
                return (
                  <tr key={item.mediaId}>
                    <td>
                      <input
                        type="checkbox"
                        checked={selectedIds.has(item.mediaId)}
                        onChange={(event) => toggleSelected(item.mediaId, event.target.checked)}
                      />
                    </td>
                    <td className={styles["title-cell"]}>
                      {editing ? (
                        <div className={styles["edit-fields"]}>
                          <input value={draftTitle} onChange={(event) => setDraftTitle(event.target.value)} />
                        </div>
                      ) : (
                        <>
                          <span className={styles["main-title"]} title={item.title || item.path}>{item.title || <em className="muted">(no title)</em>}</span>
                          <span className={styles["subline"]} title={item.mediaId}>{item.mediaId}</span>
                          <span className={styles["path-text"]} title={item.path}>{item.path}</span>
                        </>
                      )}
                    </td>
                    <td>
                      {editing ? (
                        <div className={styles["ordering-fields"]}>
                          <input
                            value={draftSeason}
                            inputMode="numeric"
                            placeholder="S"
                            aria-label="season"
                            onChange={(event) => setDraftSeason(event.target.value)}
                          />
                          <input
                            value={draftEpisode}
                            inputMode="numeric"
                            placeholder="E"
                            aria-label="episode"
                            onChange={(event) => setDraftEpisode(event.target.value)}
                          />
                        </div>
                      ) : (
                        <span className={styles["tiny-chip"]}>{episodeOrderLabel(item)}</span>
                      )}
                    </td>
                    <td className={styles["collection-cell"]}>
                      {editing ? (
                        <div className={styles["edit-fields"]}>
                          <input value={draftCollection} onChange={(event) => setDraftCollection(event.target.value)} />
                        </div>
                      ) : (
                        <span className={styles["collection-text"]} title={item.collection || "no show"}>
                          {item.collection || <em className="muted">-</em>}
                        </span>
                      )}
                    </td>
                    <td>
                      <span className={styles["subline"]}>{formatMs(item.durationMs)}</span>
                    </td>
                    <td>
                      <span className={styles["tiny-chip"]}>{resolutionLabel(item)}</span>
                    </td>
                    <td>
                      <span className={styles["tiny-chip"]}>{item.videoCodec ? item.videoCodec.toUpperCase() : "—"}</span>
                    </td>
                    <td>
                      <span className={styles["tiny-chip"]}>{item.audioCodec ? item.audioCodec.toUpperCase() : "—"}</span>
                    </td>
                    <td>
                      <div className={styles["chip-stack"]}>
                        <span className={`${styles["tiny-chip"]}${item.failedPackages > 0 ? ` ${styles["is-bad"]}` : ""}`}>
                          {packageLabel(item)}
                        </span>
                      </div>
                    </td>
                    <td>
                      <div className={styles["row-actions"]}>
                        {editing ? (
                          <>
                            <button type="button" disabled={savingId === item.mediaId} onClick={() => void saveEdit(item)}>
                              {savingId === item.mediaId ? "saving" : "save"}
                            </button>
                            <button type="button" disabled={savingId === item.mediaId} onClick={() => setEditingId(null)}>
                              cancel
                            </button>
                          </>
                        ) : (
                          <>
                            <button type="button" onClick={() => startEdit(item)}>edit</button>
                            <button type="button" disabled={deletingId === item.mediaId} onClick={() => void deleteRow(item)}>
                              {deletingId === item.mediaId ? "deleting" : "delete"}
                            </button>
                          </>
                        )}
                      </div>
                    </td>
                  </tr>
                );
              })}
              {!loading && rows.length === 0 && (
                <tr>
                  <td colSpan={10} className="muted">no media matches</td>
                </tr>
              )}
            </tbody>
          </table>
        </div>

        {rows.length < count && (
          <div className={styles["load-more"]}>
            <button type="button" disabled={loadingMore} onClick={() => void loadMore()}>
              {loadingMore ? "loading..." : `load more (${rows.length}/${count})`}
            </button>
          </div>
        )}
      </section>
    </div>
  );
}
