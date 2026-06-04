import { Fragment } from "react";
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
  onEntryClick?: (entry: Entry) => void;
  onGapClick?: (startMs: number) => void;
  disabled?: boolean;
};

export function ScheduleTimeline(props: ScheduleTimelineProps) {
  const { windowStartMs, windowHours, nowMs, entries, unanchored = false, onEntryClick, onGapClick, disabled } = props;
  const windowEndMs = windowStartMs + windowHours * HOUR_MS;
  const totalWidthPx = windowHours * HOUR_WIDTH_PX;

  function msToPx(ms: number) {
    return ((ms - windowStartMs) / HOUR_MS) * HOUR_WIDTH_PX;
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

  const nowVisible = !unanchored && nowMs >= windowStartMs && nowMs <= windowEndMs;

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
        <div className="schedule-timeline-track">
          {blocks.map((block, idx) => {
            const leftPx = msToPx(block.startMs);
            const widthPx = Math.max(msToPx(block.endMs) - leftPx, 2);
            const durationMs = block.endMs - block.startMs;
            const isNow = block.startMs <= nowMs && nowMs < block.endMs;
            if (block.kind === "gap") {
              return (
                <button
                  key={`gap-${block.startMs}-${idx}`}
                  type="button"
                  className={`schedule-timeline-gap${isNow ? " is-now" : ""}`}
                  style={{ left: `${leftPx}px`, width: `${widthPx}px` }}
                  disabled={disabled || !onGapClick}
                  onClick={() => onGapClick?.(block.startMs)}
                  title={`Fill gap at ${formatTimelinePoint(block.startMs, windowStartMs, unanchored)} · ${formatMs(durationMs)}`}
                >
                  {widthPx >= MIN_BLOCK_PX && (
                    <span className="schedule-timeline-gap-label">gap · {formatMs(durationMs)}</span>
                  )}
                </button>
              );
            }
            const entry = block.entry;
            return (
              <button
                key={entry.entryId}
                type="button"
                className={`schedule-timeline-entry${isNow ? " is-now" : ""}`}
                style={{ left: `${leftPx}px`, width: `${widthPx}px` }}
                disabled={disabled || !onEntryClick}
                onClick={() => onEntryClick?.(entry)}
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
