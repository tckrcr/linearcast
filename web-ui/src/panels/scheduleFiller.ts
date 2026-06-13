import { SCHEDULE_GRID_MS } from "../constants";
import type { ScheduleBuilderEntryInput } from "../api/scheduleBuilder";

// Returns ms when already on the slot boundary, otherwise the next boundary
// after ms. slotMs must be a positive multiple of the segment grid.
export function alignToSlot(ms: number, slotMs: number) {
  const rem = ms % slotMs;
  return rem === 0 ? ms : ms + (slotMs - rem);
}

export function clipToGrid(ms: number) {
  return ms - (ms % SCHEDULE_GRID_MS);
}

export type FillerMeta = { mediaId: string; packagedDurationMs: number; title: string };

// Gap (ms) left after each primary before the next slot boundary, keyed by the
// primary's draftId. The last primary has no trailing gap — the schedule just
// ends — so it is absent from the map. Primaries that already end on a boundary
// map to 0.
export function gapAfterByPrimary(
  primaries: { draftId: string; durationMs: number }[],
  slotMs: number,
): Map<string, number> {
  const out = new Map<string, number>();
  let cursor = 0;
  for (let i = 0; i < primaries.length; i++) {
    const start = alignToSlot(cursor, slotMs);
    if (i > 0) out.set(primaries[i - 1].draftId, start - cursor);
    cursor = start + primaries[i].durationMs;
  }
  return out;
}

export type SlotGridComposition = {
  entries: ScheduleBuilderEntryInput[];
  fillerMediaIds: string[];
  unfilledGapCount: number;
  unfilledGapMs: number;
  filledGapMs: number;
};

// Lays primaries on slot boundaries and fills each inter-slot gap with the filler
// the user assigned to the preceding episode (assignment: draftId -> filler
// mediaId). A long filler assigned to several gaps rotates through itself so it
// doesn't replay its opening each time. The result is the explicit, gap-free
// schedule the server persists verbatim; a gap whose assigned filler is missing
// or too short is reported as unfilled so the UI can block creation. Entries are
// 0-based and contiguous — the server rebases them onto the wall-clock slot
// boundary at create time.
export function composeSlotGridEntries(
  primaries: { draftId: string; mediaId: string; durationMs: number }[],
  slotMs: number,
  assignment: Map<string, string>,
  fillerById: Map<string, FillerMeta>,
): SlotGridComposition {
  const entries: ScheduleBuilderEntryInput[] = [];
  const usedFiller = new Set<string>();
  const rotation = new Map<string, number>();
  let cursor = 0;
  let unfilledGapCount = 0;
  let unfilledGapMs = 0;
  let filledGapMs = 0;
  for (let i = 0; i < primaries.length; i++) {
    const p = primaries[i];
    const start = alignToSlot(cursor, slotMs);
    const gap = start - cursor;
    if (gap > 0) {
      // The gap precedes primary i, so it belongs to primary i-1.
      const fillerId = assignment.get(primaries[i - 1].draftId);
      const filler = fillerId ? fillerById.get(fillerId) : undefined;
      if (filler && filler.packagedDurationMs >= gap) {
        let offsetMs = rotation.get(filler.mediaId) ?? 0;
        if (offsetMs + gap > filler.packagedDurationMs) offsetMs = 0;
        rotation.set(filler.mediaId, offsetMs + gap);
        entries.push({ mediaId: filler.mediaId, offsetMs, durationMs: gap });
        usedFiller.add(filler.mediaId);
        filledGapMs += gap;
      } else {
        unfilledGapCount++;
        unfilledGapMs += gap;
      }
    }
    entries.push({ mediaId: p.mediaId, offsetMs: 0, durationMs: p.durationMs });
    cursor = start + p.durationMs;
  }
  return { entries, fillerMediaIds: [...usedFiller], unfilledGapCount, unfilledGapMs, filledGapMs };
}
