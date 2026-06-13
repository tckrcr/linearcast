import { Fragment, useRef, useState } from "react";
import { MEDIA_DRAG_MIME, SCHEDULE_BATCH_DRAG_MIME } from "../constants";
import { formatDateTime, formatMs } from "../format";
import type { ChannelSchedule } from "../types";

const HOUR_MS = 3600 * 1000;
const HOUR_WIDTH_PX = 120;
const MIN_BLOCK_PX = 28;

type Entry = ChannelSchedule["entries"][number];

export type ScheduleTimelineProps = {
  windowStartMs: number;
  windowHours: number;
  nowMs: number;
  entries: Entry[];
  unanchored?: boolean;
  // Maps a gap's start (ms, in the same coordinate space as entries) to the
  // label of the filler assigned to it, so a planned-but-not-yet-saved fill
  // renders as a filled block instead of an empty gap.
  filledGaps?: Map<number, string>;
  onEntryClick?: (entry: Entry) => void;
  onGapClick?: (startMs: number) => void;
  onFillGapMedia?: (mediaKey: string, startMs: number, endMs: number) => void;
  // When provided, entry blocks become drag-to-reorder handles. The indices are
  // positions within the `entries` array.
  onReorder?: (fromIndex: number, toIndex: number) => void;
  // When provided, media dragged from a picker rail (carrying MEDIA_DRAG_MIME)
  // can be dropped onto the track to insert at the pointed-to position.
  onInsertMedia?: (mediaKey: string, index: number) => void;
  // When provided, batch picker items (group/show/album/artist payloads carrying
  // SCHEDULE_BATCH_DRAG_MIME) can be dropped at the pointed-to position.
  onInsertBatch?: (payload: string, index: number) => void;
  disabled?: boolean;
};

export function ScheduleTimeline(props: ScheduleTimelineProps) {
  const { windowStartMs, windowHours, nowMs, entries, unanchored = false, filledGaps, onEntryClick, onGapClick, onFillGapMedia, onReorder, onInsertMedia, onInsertBatch, disabled } = props;
  const reorderable = !disabled && !!onReorder;
  const insertable = !disabled && (!!onInsertMedia || !!onInsertBatch);
  const droppable = reorderable || insertable;
  const trackRef = useRef<HTMLDivElement>(null);
  const [dragIndex, setDragIndex] = useState<number | null>(null);
  const [insertionIndex, setInsertionIndex] = useState<number | null>(null);

  const windowEndMs = windowStartMs + windowHours * HOUR_MS;
  const totalWidthPx = windowHours * HOUR_WIDTH_PX;

  function msToPx(ms: number) {
    return ((ms - windowStartMs) / HOUR_MS) * HOUR_WIDTH_PX;
  }

  function endDrag() {
    setDragIndex(null);
    setInsertionIndex(null);
  }

  // Translate a pointer x-coordinate into an insertion slot (0..entries.length)
  // by comparing against each entry's horizontal midpoint.
  function computeInsertionIndex(clientX: number): number {
    const track = trackRef.current;
    if (!track) return entries.length;
    const x = clientX - track.getBoundingClientRect().left;
    for (let i = 0; i < entries.length; i++) {
      const mid = (msToPx(entries[i].startMs) + msToPx(entries[i].endMs)) / 2;
      if (x < mid) return i;
    }
    return entries.length;
  }

  // Hour ticks across the window.
  const ticks: number[] = [];
  for (let h = 0; h <= windowHours; h++) {
    ticks.push(windowStartMs + h * HOUR_MS);
  }

  // Build a flat list of blocks (entries + gaps), clipped to the window.
  type Block =
    | { kind: "entry"; entry: Entry; startMs: number; endMs: number }
    | { kind: "gap"; startMs: number; endMs: number };

  const blocks: Block[] = [];
  let cursor = windowStartMs;
  for (const entry of entries) {
    const startMs = Math.max(entry.startMs, windowStartMs);
    const endMs = Math.min(entry.endMs, windowEndMs);
    if (endMs <= windowStartMs || startMs >= windowEndMs) continue;
    if (startMs > cursor) {
      blocks.push({ kind: "gap", startMs: cursor, endMs: startMs });
    }
    blocks.push({ kind: "entry", entry, startMs, endMs });
    cursor = Math.max(cursor, endMs);
  }
  if (cursor < windowEndMs) {
    blocks.push({ kind: "gap", startMs: cursor, endMs: windowEndMs });
  }

  // Map each entry object back to its index in the source array so reorder
  // callbacks can address the underlying draft positions.
  const entryIndex = new Map<Entry, number>(entries.map((entry, idx) => [entry, idx]));

  const nowVisible = !unanchored && nowMs >= windowStartMs && nowMs <= windowEndMs;

  // Pixel position of the insertion marker while a drop is in progress.
  let insertMarkerPx: number | null = null;
  if (insertionIndex !== null) {
    if (insertionIndex >= entries.length) {
      insertMarkerPx = entries.length > 0 ? msToPx(entries[entries.length - 1].endMs) : 0;
    } else {
      insertMarkerPx = msToPx(entries[insertionIndex].startMs);
    }
  }

  return (
    <div className="schedule-timeline-scroll">
      <div
        className="schedule-timeline"
        style={{ width: `${totalWidthPx}px` }}
      >
        <div className="schedule-timeline-rail">
          {ticks.map((tickMs, idx) => (
            <Fragment key={tickMs}>
              <div
                className="schedule-timeline-tick"
                style={{ left: `${msToPx(tickMs)}px` }}
              />
              {idx < ticks.length - 1 && (
                <div
                  className="schedule-timeline-tick-label"
                  style={{ left: `${msToPx(tickMs)}px` }}
                >
                  {unanchored ? formatRelativeLabel(tickMs - windowStartMs) : formatHourLabel(tickMs)}
                </div>
              )}
            </Fragment>
          ))}
        </div>
        <div
          ref={trackRef}
          className="schedule-timeline-track"
          onDragOver={droppable ? (e) => {
            const external = insertable && (e.dataTransfer.types.includes(MEDIA_DRAG_MIME) || e.dataTransfer.types.includes(SCHEDULE_BATCH_DRAG_MIME));
            const internal = reorderable && dragIndex !== null;
            if (!external && !internal) return;
            e.preventDefault();
            e.dataTransfer.dropEffect = external ? "copy" : "move";
            const idx = computeInsertionIndex(e.clientX);
            if (idx !== insertionIndex) setInsertionIndex(idx);
          } : undefined}
          onDragLeave={droppable ? (e) => {
            if (!trackRef.current?.contains(e.relatedTarget as Node | null)) setInsertionIndex(null);
          } : undefined}
          onDrop={droppable ? (e) => {
            e.preventDefault();
            const idx = insertionIndex ?? computeInsertionIndex(e.clientX);
            const batchPayload = insertable ? e.dataTransfer.getData(SCHEDULE_BATCH_DRAG_MIME) : "";
            const key = insertable ? e.dataTransfer.getData(MEDIA_DRAG_MIME) : "";
            if (batchPayload && onInsertBatch) {
              onInsertBatch(batchPayload, idx);
            } else if (key && onInsertMedia) {
              onInsertMedia(key, idx);
            } else if (reorderable && dragIndex !== null) {
              const target = dragIndex < idx ? idx - 1 : idx;
              if (target !== dragIndex) onReorder!(dragIndex, target);
            }
            endDrag();
          } : undefined}
        >
          {blocks.map((block, idx) => {
            const leftPx = msToPx(block.startMs);
            const widthPx = Math.max(msToPx(block.endMs) - leftPx, 2);
            const durationMs = block.endMs - block.startMs;
            const isNow = block.startMs <= nowMs && nowMs < block.endMs;
            if (block.kind === "gap") {
              const fillerLabel = filledGaps?.get(block.startMs);
              if (fillerLabel) {
                return (
                  <div
                    key={`fill-${block.startMs}-${idx}`}
                    className={`schedule-timeline-entry is-filler${isNow ? " is-now" : ""}`}
                    style={{ left: `${leftPx}px`, width: `${widthPx}px` }}
                    title={`filler · ${fillerLabel} · ${formatMs(durationMs)}`}
                  >
                    {widthPx >= MIN_BLOCK_PX && (
                      <span className="schedule-timeline-entry-title">{fillerLabel}</span>
                    )}
                  </div>
                );
              }
              return (
                <button
                  key={`gap-${block.startMs}-${idx}`}
                  type="button"
                  className={`schedule-timeline-gap${isNow ? " is-now" : ""}`}
                  style={{ left: `${leftPx}px`, width: `${widthPx}px` }}
                  disabled={disabled || (!onGapClick && !onFillGapMedia)}
                  onClick={() => onGapClick?.(block.startMs)}
                  onDragOver={onFillGapMedia ? (e) => {
                    if (!e.dataTransfer.types.includes(MEDIA_DRAG_MIME)) return;
                    e.preventDefault();
                    e.dataTransfer.dropEffect = "copy";
                  } : undefined}
                  onDrop={onFillGapMedia ? (e) => {
                    const key = e.dataTransfer.getData(MEDIA_DRAG_MIME);
                    if (!key) return;
                    e.preventDefault();
                    e.stopPropagation();
                    onFillGapMedia(key, block.startMs, block.endMs);
                    endDrag();
                  } : undefined}
                  title={`Fill gap at ${formatTimelinePoint(block.startMs, windowStartMs, unanchored)} · ${formatMs(durationMs)}`}
                >
                  {widthPx >= MIN_BLOCK_PX && (
                    <span className="schedule-timeline-gap-label">gap · {formatMs(durationMs)}</span>
                  )}
                </button>
              );
            }
            const entry = block.entry;
            const entryIdx = entryIndex.get(entry) ?? -1;
            const isDragging = reorderable && entryIdx === dragIndex;
            return (
              <button
                key={entry.entryId}
                type="button"
                className={`schedule-timeline-entry${isNow ? " is-now" : ""}${reorderable ? " is-reorderable" : ""}${isDragging ? " is-dragging" : ""}`}
                style={{ left: `${leftPx}px`, width: `${widthPx}px` }}
                disabled={disabled || (!onEntryClick && !reorderable)}
                draggable={reorderable && entryIdx >= 0}
                onClick={() => onEntryClick?.(entry)}
                onDragStart={reorderable ? (e) => {
                  setDragIndex(entryIdx);
                  e.dataTransfer.effectAllowed = "move";
                  e.dataTransfer.setData("text/plain", String(entryIdx));
                } : undefined}
                onDragEnd={reorderable ? endDrag : undefined}
                title={`${formatTimelinePoint(entry.startMs, windowStartMs, unanchored)} · ${entry.title || entry.mediaId} · ${formatMs(entry.durationMs)}`}
              >
                <span className="schedule-timeline-entry-title">
                  {entry.title || entry.mediaId}
                </span>
                {widthPx >= MIN_BLOCK_PX * 2 && (
                  <span className="schedule-timeline-entry-meta muted">
                    {formatMs(entry.durationMs)}
                    {entry.schedulingGroup && ` · ${entry.schedulingGroup}`}
                  </span>
                )}
              </button>
            );
          })}
          {insertMarkerPx !== null && (
            <div
              className="schedule-timeline-insert-marker"
              style={{ left: `${insertMarkerPx}px` }}
              aria-hidden
            />
          )}
          {nowVisible && (
            <div
              className="schedule-timeline-now"
              style={{ left: `${msToPx(nowMs)}px` }}
              aria-label="now"
            />
          )}
        </div>
      </div>
    </div>
  );
}

function formatTimelinePoint(ms: number, windowStartMs: number, unanchored: boolean): string {
  return unanchored ? formatRelativeLabel(ms - windowStartMs) : formatDateTime(ms);
}

function formatRelativeLabel(ms: number): string {
  const totalMinutes = Math.max(0, Math.floor(ms / 60000));
  const hours = Math.floor(totalMinutes / 60);
  const minutes = totalMinutes % 60;
  return `${hours}:${String(minutes).padStart(2, "0")}`;
}

function formatHourLabel(ms: number): string {
  const d = new Date(ms);
  const h = d.getHours();
  if (h === 0) {
    return `${d.toLocaleDateString(undefined, { month: "short", day: "numeric" })} · 12a`;
  }
  if (h === 12) return "12p";
  return h < 12 ? `${h}a` : `${h - 12}p`;
}
