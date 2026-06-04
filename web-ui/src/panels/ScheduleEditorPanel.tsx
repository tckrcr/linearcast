import { Fragment, useEffect, useState } from "react";
import { formatDateTime, formatMs } from "../format";
import { useScheduleEditor, WINDOW_HOURS } from "../hooks/useScheduleEditor";
import type { ChannelNow, ScheduleDraftEntry } from "../types";
import type { PickerRailItem } from "./MediaPickerRail";
import { SchedulePickerRail, type SchedulePickerRailTab } from "./SchedulePickerRail";
import { ScheduleTimeline } from "./ScheduleTimeline";

type ScheduleView = "list" | "timeline";
const SCHEDULE_VIEW_KEY = "tc.scheduleView";

export function ScheduleEditorPanel({ channel }: { channel: ChannelNow }) {
  const {
    scheduleWindowStart,
    scheduleData,
    scheduleLoading,
    scheduleError,
    scheduleNotice,
    deletingEntries,
    selectedRangeStarts,
    rangeBusy,
    mediaData,
    mediaLoading,
    mediaError,
    editTarget,
    mediaFilter,
    setMediaFilter,
    insertBusy,
    scheduleEditMode,
    scheduleDraft,
    scheduleDraftWindow,
    draftInsertAt,
    dragIndex,
    saveBusy,
    schedulePreview,
    previewBusy,
    applyPreviewBusy,
    shiftWindow,
    jumpToNow,
    openReplace,
    openFill,
    openChoose,
    chooseInsertAfter,
    chooseInsertBefore,
    insertMedia,
    insertRelativeEntry,
    selectScheduleEntry,
    clearScheduleSelection,
    deleteSelectedEntries,
    beginScheduleEdit,
    beginPreviewEdit,
    cancelScheduleEdit,
    moveDraftEntry,
    removeDraftEntry,
    appendDraftEntry,
    undoScheduleDraftChange,
    saveScheduleEdit,
    previewScheduleRebuild,
    clearSchedulePreview,
    applySchedulePreview,
    loadMedia,
    deleteEntry,
    setDraftInsertAt,
    setEditTarget,
    setDragIndex,
    nowMs,
    windowLabel,
    mutationBusy,
    filteredReadyMedia,
    visibleScheduleEntries,
    canUndoScheduleDraft,
  } = useScheduleEditor(channel);

  const [view, setView] = useState<ScheduleView>(() => {
    try {
      const stored = window.localStorage.getItem(SCHEDULE_VIEW_KEY);
      return stored === "timeline" ? "timeline" : "list";
    } catch {
      return "list";
    }
  });
  useEffect(() => {
    try {
      window.localStorage.setItem(SCHEDULE_VIEW_KEY, view);
    } catch {
      /* localStorage may be unavailable */
    }
  }, [view]);
  // Timeline is read-only for now; force list during edit mode.
  const effectiveView: ScheduleView = scheduleEditMode ? "list" : view;

  return (
    <>
      {/* Schedule */}
      <section className="admin-panel-section">
        <div className="section-headline">
          <h3>Schedule</h3>
          <div className="schedule-view-toggle" role="tablist" aria-label="Schedule view">
            <button
              type="button"
              role="tab"
              aria-selected={view === "list"}
              className={`schedule-view-tab${view === "list" ? " is-active" : ""}`}
              disabled={scheduleEditMode}
              onClick={() => setView("list")}
            >
              List
            </button>
            <button
              type="button"
              role="tab"
              aria-selected={view === "timeline"}
              className={`schedule-view-tab${view === "timeline" ? " is-active" : ""}`}
              disabled={scheduleEditMode}
              title={scheduleEditMode ? "Switch back to read mode to use the timeline" : undefined}
              onClick={() => setView("timeline")}
            >
              Timeline
            </button>
          </div>
          <div className="schedule-nav">
            <button type="button" disabled={scheduleEditMode} onClick={() => shiftWindow(-WINDOW_HOURS)}>
              ← prev
            </button>
            <button type="button" disabled={scheduleEditMode} onClick={jumpToNow}>
              now
            </button>
            <button type="button" disabled={scheduleEditMode} onClick={() => shiftWindow(WINDOW_HOURS)}>
              next →
            </button>
          </div>
        </div>
        <p className="muted schedule-window-label">{windowLabel}</p>
        <div className="schedule-edit-bar">
          {!scheduleEditMode && (
            <>
              <button
                type="button"
                className="primary"
                disabled={!scheduleData || scheduleData.entries.length === 0 || mutationBusy}
                onClick={beginScheduleEdit}
              >
                Edit
              </button>
              <button
                type="button"
                className="danger"
                disabled={selectedRangeStarts.size === 0 || mutationBusy}
                onClick={() => void deleteSelectedEntries()}
              >
                {rangeBusy ? "…" : `Delete selected (${selectedRangeStarts.size})`}
              </button>
              {selectedRangeStarts.size > 0 && (
                <button type="button" disabled={mutationBusy} onClick={clearScheduleSelection}>
                  Clear selection
                </button>
              )}
            </>
          )}
          {scheduleEditMode && (
            <>
              <button type="button" className="primary" disabled={scheduleDraft.length === 0 || mutationBusy} onClick={() => void saveScheduleEdit()}>
                {saveBusy ? "…" : "Save"}
              </button>
              <button type="button" disabled={mutationBusy} onClick={cancelScheduleEdit}>
                Revert
              </button>
              <button type="button" disabled={mutationBusy || !canUndoScheduleDraft} onClick={undoScheduleDraftChange}>
                Undo
              </button>
              <button
                type="button"
                disabled={previewBusy || mutationBusy}
                onClick={() => void previewScheduleRebuild()}
              >
                {previewBusy ? "…" : "Preview rebuild"}
              </button>
              <button
                type="button"
                disabled={mutationBusy}
                onClick={() => {
                  setDraftInsertAt(scheduleDraft.length - 1);
                  setEditTarget(null);
                  void loadMedia(false);
                }}
              >
                Add entry
              </button>
              <span className="muted">editing {scheduleDraft.length} rows</span>
            </>
          )}
        </div>
        {editTarget?.mode === "choose" && (
          <div className="schedule-action-chooser">
            <div className="schedule-action-chooser-head">
              <div>
                <strong>{editTarget.label}</strong>
                {editTarget.isCurrentlyPlaying && (
                  <span className="muted"> · currently playing</span>
                )}
              </div>
              <button
                type="button"
                onClick={() => setEditTarget(null)}
              >
                Cancel
              </button>
            </div>
            <div className="schedule-action-chooser-actions">
              <button
                type="button"
                className="primary"
                disabled={!editTarget.canInsertBefore || mutationBusy}
                title={
                  editTarget.canInsertBefore
                    ? "Queue something to play before this entry starts"
                    : "This entry has already started"
                }
                onClick={() => chooseInsertBefore(editTarget.entryId, `before ${editTarget.label}`)}
              >
                Insert before
              </button>
              <button
                type="button"
                className="primary"
                disabled={!editTarget.canInsertAfter || mutationBusy}
                title={
                  editTarget.canInsertAfter
                    ? "Queue something to play after this entry finishes"
                    : "This entry has already ended"
                }
                onClick={() => chooseInsertAfter(editTarget.entryId, `after ${editTarget.label}`)}
              >
                Insert after
              </button>
              <button
                type="button"
                disabled={mutationBusy}
                title={
                  editTarget.isCurrentlyPlaying
                    ? "Stop this entry and play a different one at this slot"
                    : "Replace this entry"
                }
                onClick={() => {
                  if (editTarget.isCurrentlyPlaying) {
                    const ok = window.confirm(
                      `Jump away from the currently-playing entry?`,
                    );
                    if (!ok) return;
                  }
                  setEditTarget({
                    mode: "jump",
                    startMs: editTarget.startMs,
                    label: editTarget.label,
                  });
                  void loadMedia();
                }}
              >
                Jump (replace)
              </button>
              <button
                type="button"
                className="danger"
                disabled={mutationBusy}
                onClick={() => {
                  const id = editTarget.entryId;
                  setEditTarget(null);
                  deleteEntry(id);
                }}
              >
                Delete
              </button>
            </div>
          </div>
        )}
        {((editTarget && editTarget.mode !== "choose") || draftInsertAt !== null) && (() => {
          const mediaRows = filteredReadyMedia.slice(0, 80);
          const mediaItems: PickerRailItem[] = mediaRows.map((media) => ({
            key: media.mediaId,
            title: media.title || media.mediaId,
            meta: media.schedulingGroup,
            durationMs: media.packagedDurationMs ?? media.durationMs,
          }));

          function handleMediaAction(key: string) {
            const media = mediaRows.find((m) => m.mediaId === key);
            if (!media) return;
            if (draftInsertAt !== null) appendDraftEntry(media);
            else if (editTarget?.mode === "insert-before") void insertRelativeEntry(editTarget.beforeEntryId, media, "before");
            else if (editTarget?.mode === "insert-after") void insertRelativeEntry(editTarget.afterEntryId, media, "after");
            else void insertMedia(media);
          }

          let title: string;
          if (draftInsertAt !== null) {
            title = "Add to draft";
          } else if (editTarget?.mode === "jump") {
            title = "Jump to:";
          } else if (editTarget?.mode === "fill") {
            title = "Fill";
          } else if (editTarget?.mode === "insert-before") {
            title = "Insert before:";
          } else if (editTarget?.mode === "insert-after") {
            title = "Insert after:";
          } else {
            title = "";
          }
          const subtitle = draftInsertAt !== null ? "at end" : editTarget?.label;
          const pickerTabs: SchedulePickerRailTab[] = [
            {
              id: "media",
              label: "Episodes",
              query: mediaFilter,
              onQueryChange: setMediaFilter,
              queryPlaceholder: "Filter ready media",
              searchDisabled: insertBusy,
              onRefresh: () => void loadMedia(true),
              refreshing: mediaLoading,
              refreshLabel: "Refresh media",
              loading: mediaLoading,
              loadingMessage: "loading media…",
              error: mediaError,
              items: mediaItems,
              onItemAction: handleMediaAction,
              itemActionBusy: insertBusy,
              emptyMessage: mediaData ? "no ready media matches" : undefined,
            },
          ];

          return (
            <SchedulePickerRail
              title={title}
              subtitle={subtitle}
              onClose={() => {
                setEditTarget(null);
                setDraftInsertAt(null);
              }}
              tabs={pickerTabs}
              activeTab="media"
              onTabChange={() => {
                void loadMedia(false);
              }}
            />
          );
        })()}
        {scheduleLoading && <p className="muted">loading…</p>}
        {scheduleError && <p className="error">{scheduleError}</p>}
        {scheduleNotice && <p className="success">{scheduleNotice}</p>}
        {schedulePreview && (
          <div className="schedule-preview">
            <div className="schedule-preview-head">
              <div>
                <strong>Preview rebuild</strong>
                <span className="muted">
                  {schedulePreview.count} rows · {schedulePreview.profile} · {formatDateTime(schedulePreview.fromMs)} – {formatDateTime(schedulePreview.toMs)}
                </span>
              </div>
              <div className="schedule-preview-actions">
                {!scheduleEditMode && (
                  <button
                    type="button"
                    className="primary"
                    disabled={applyPreviewBusy || mutationBusy || schedulePreview.entries.length === 0}
                    onClick={() => void applySchedulePreview()}
                  >
                    {applyPreviewBusy ? "…" : "Apply"}
                  </button>
                )}
                <button
                  type="button"
                  className={scheduleEditMode ? "primary" : undefined}
                  disabled={applyPreviewBusy || mutationBusy || schedulePreview.entries.length === 0}
                  onClick={beginPreviewEdit}
                >
                  {scheduleEditMode ? "Use as draft" : "Edit preview"}
                </button>
                <button type="button" disabled={applyPreviewBusy || mutationBusy} onClick={clearSchedulePreview}>
                  Dismiss
                </button>
              </div>
            </div>
            <div className="schedule-preview-stats">
              <span>{schedulePreview.eligibleReadyMedia}/{schedulePreview.eligibleMedia} ready</span>
              <span>{schedulePreview.diff.unchanged} unchanged</span>
              <span>{schedulePreview.diff.added.length} added</span>
              <span>{schedulePreview.diff.removed.length} removed</span>
              {schedulePreview.entries.length > 0 && schedulePreview.generatedEndMs < schedulePreview.toMs && (
                <span>ends {formatDateTime(schedulePreview.generatedEndMs)}</span>
              )}
            </div>
            {schedulePreview.warnings.length > 0 && (
              <ul className="schedule-preview-warnings">
                {schedulePreview.warnings.map((warning) => (
                  <li key={`${warning.code}-${warning.message}`}>{warning.message}</li>
                ))}
              </ul>
            )}
            {schedulePreview.entries.length === 0 && (
              <p className="muted">preview did not generate any schedule rows</p>
            )}
          </div>
        )}
        {!scheduleLoading && scheduleData && visibleScheduleEntries.length === 0 && (
          <div className="schedule-empty-state">
            <p className="muted">no scheduled entries in this window</p>
            <button type="button" disabled={mutationBusy || scheduleEditMode} onClick={() => openFill(scheduleWindowStart)}>
              Fill window start
            </button>
          </div>
        )}
        {scheduleData && visibleScheduleEntries.length > 0 && effectiveView === "timeline" && (
          <ScheduleTimeline
            windowStartMs={scheduleWindowStart}
            windowHours={WINDOW_HOURS}
            nowMs={nowMs}
            entries={scheduleData.entries}
            onEntryClick={(entry) => openChoose(entry)}
            onGapClick={(startMs) => openFill(startMs)}
            disabled={mutationBusy}
          />
        )}
        {scheduleData && visibleScheduleEntries.length > 0 && effectiveView === "list" && (
          <ul className="schedule-list">
            {visibleScheduleEntries.map((entry, idx) => {
              const displayStartMs = scheduleEditMode
                ? visibleScheduleEntries.slice(0, idx).reduce((ms, row) => ms + row.durationMs, scheduleDraftWindow?.fromMs ?? entry.startMs)
                : entry.startMs;
              const displayEndMs = displayStartMs + entry.durationMs;
              const prevEnd = idx > 0 ? visibleScheduleEntries[idx - 1].endMs : scheduleWindowStart;
              const rawGapStart = !scheduleEditMode && prevEnd < entry.startMs ? prevEnd : null;
              const gapContainsNow = rawGapStart != null && rawGapStart <= nowMs && nowMs < entry.startMs;
              // Suppress the leading gap when we've reached or passed the gap start
              // and the channel is playing — the gap is either behind us or we're
              // inside it, either way content is being served and the Fill button
              // is not useful.
              const gapStart = rawGapStart != null && !(idx === 0 && channel.status === "playing" && nowMs >= rawGapStart)
                ? rawGapStart
                : null;
              const isNow = displayStartMs <= nowMs && nowMs < displayEndMs;
              const isDeleting = deletingEntries.has(entry.entryId);
              const isSelected = selectedRangeStarts.has(entry.entryId);
              return (
                <Fragment key={scheduleEditMode ? (entry as ScheduleDraftEntry).draftId : entry.entryId}>
                  {gapStart != null && (
                    <li key={`gap-${gapStart}-${entry.startMs}`} className={`schedule-gap-row${gapContainsNow ? " is-now" : ""}`}>
                      <span className="schedule-time">{formatDateTime(gapStart)}</span>
                      <span className="schedule-title">{gapContainsNow ? "gap (now)" : "gap"}</span>
                      <span className="schedule-meta muted">{formatMs(entry.startMs - gapStart)}</span>
                      <button type="button" disabled={mutationBusy} onClick={() => openFill(gapStart)}>
                        Fill
                      </button>
                    </li>
                  )}
                  <li
                    key={scheduleEditMode ? (entry as ScheduleDraftEntry).draftId : entry.entryId}
                    className={`schedule-entry${isNow ? " is-now" : ""}${isSelected ? " is-selected" : ""}${scheduleEditMode ? " is-editing" : ""}`}
                    draggable={scheduleEditMode && !mutationBusy}
                    onDragStart={() => setDragIndex(idx)}
                    onDragOver={(event) => {
                      if (!scheduleEditMode) return;
                      event.preventDefault();
                    }}
                    onDrop={(event) => {
                      event.preventDefault();
                      if (dragIndex != null) moveDraftEntry(dragIndex, idx);
                      setDragIndex(null);
                    }}
                    onDragEnd={() => setDragIndex(null)}
                  >
                    {scheduleEditMode ? (
                      <span className="schedule-drag-handle" title="Drag to reorder">↕</span>
                    ) : (
                      <label className="schedule-range-select" title="Select for range delete">
                        <input
                          type="checkbox"
                          checked={isSelected}
                          disabled={mutationBusy}
                          onChange={(event) => {
                            const me = event.nativeEvent instanceof MouseEvent ? event.nativeEvent : null;
                            selectScheduleEntry(entry.entryId, idx, {
                              range: me?.shiftKey ?? false,
                              additive: me ? (me.ctrlKey || me.metaKey) : false,
                            });
                          }}
                        />
                      </label>
                    )}
                    <span className="schedule-time">{formatDateTime(displayStartMs)}</span>
                    <span className="schedule-title">{entry.title || entry.mediaId}</span>
                    <span className="schedule-meta muted">
                      {formatMs(entry.durationMs)}
                      {entry.schedulingGroup && ` · ${entry.schedulingGroup}`}
                    </span>
                    {scheduleEditMode ? (
                      <div className="schedule-entry-move">
                        <button type="button" disabled={idx === 0 || mutationBusy} onClick={() => moveDraftEntry(idx, idx - 1)}>
                          ↑
                        </button>
                        <button type="button" disabled={idx === visibleScheduleEntries.length - 1 || mutationBusy} onClick={() => moveDraftEntry(idx, idx + 1)}>
                          ↓
                        </button>
                        <button
                          type="button"
                          title="Add entry at end"
                          disabled={mutationBusy}
                          onClick={() => {
                            setDraftInsertAt(scheduleDraft.length - 1);
                            setEditTarget(null);
                            void loadMedia(false);
                          }}
                        >
                          +
                        </button>
                      </div>
                    ) : (
                      <button
                        type="button"
                        className="schedule-entry-action"
                        disabled={mutationBusy}
                        onClick={() => openReplace(entry)}
                      >
                        Jump
                      </button>
                    )}
                    <button
                      type="button"
                      className="schedule-entry-delete"
                      disabled={isDeleting || mutationBusy}
                      title={scheduleEditMode ? "Remove from draft" : isNow ? "Delete the currently-playing entry" : "Delete this schedule entry"}
                      onClick={() => scheduleEditMode ? removeDraftEntry(idx) : deleteEntry(entry.entryId)}
                    >
                      {isDeleting ? "…" : "×"}
                    </button>
                  </li>
                </Fragment>
              );
            })}
          </ul>
        )}
      </section>
    </>
  );
}
