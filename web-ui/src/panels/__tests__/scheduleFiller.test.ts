import { describe, expect, it } from "vitest";
import { composeSlotGridEntries, gapAfterByPrimary, type FillerMeta } from "../scheduleFiller";

const SLOT = 30 * 60 * 1000; // 30 minutes
const MIN = 60 * 1000;

function fillerMap(items: FillerMeta[]): Map<string, FillerMeta> {
  return new Map(items.map((f) => [f.mediaId, f]));
}

describe("gapAfterByPrimary", () => {
  it("reports the trailing gap per episode and omits the last", () => {
    const gaps = gapAfterByPrimary(
      [
        { draftId: "p0", durationMs: 18 * MIN },
        { draftId: "p1", durationMs: 30 * MIN },
        { draftId: "p2", durationMs: 18 * MIN },
      ],
      SLOT,
    );
    expect(gaps.get("p0")).toBe(12 * MIN); // 18m episode → 12m to the boundary
    expect(gaps.get("p1")).toBe(0); // exact slot, no gap
    expect(gaps.has("p2")).toBe(false); // last episode has no trailing gap
  });
});

describe("composeSlotGridEntries", () => {
  it("emits primaries only when they already tile the slot grid", () => {
    const res = composeSlotGridEntries(
      [
        { draftId: "p0", mediaId: "a", durationMs: SLOT },
        { draftId: "p1", mediaId: "b", durationMs: SLOT },
      ],
      SLOT,
      new Map(),
      new Map(),
    );
    expect(res.unfilledGapCount).toBe(0);
    expect(res.fillerMediaIds).toEqual([]);
    expect(res.entries).toEqual([
      { mediaId: "a", offsetMs: 0, durationMs: SLOT },
      { mediaId: "b", offsetMs: 0, durationMs: SLOT },
    ]);
  });

  it("fills each gap with the filler assigned to the preceding episode, rotating it", () => {
    const res = composeSlotGridEntries(
      [
        { draftId: "p0", mediaId: "a", durationMs: 18 * MIN },
        { draftId: "p1", mediaId: "b", durationMs: 18 * MIN },
        { draftId: "p2", mediaId: "c", durationMs: 18 * MIN },
      ],
      SLOT,
      new Map([
        ["p0", "f"],
        ["p1", "f"],
      ]),
      fillerMap([{ mediaId: "f", packagedDurationMs: SLOT, title: "Bumper" }]),
    );
    expect(res.unfilledGapCount).toBe(0);
    expect(res.fillerMediaIds).toEqual(["f"]);
    expect(res.entries).toEqual([
      { mediaId: "a", offsetMs: 0, durationMs: 18 * MIN },
      { mediaId: "f", offsetMs: 0, durationMs: 12 * MIN },
      { mediaId: "b", offsetMs: 0, durationMs: 18 * MIN },
      { mediaId: "f", offsetMs: 12 * MIN, durationMs: 12 * MIN }, // rotation advances
      { mediaId: "c", offsetMs: 0, durationMs: 18 * MIN },
    ]);
    let cursor = 0;
    for (const e of res.entries) {
      if (e.mediaId !== "f") expect(cursor % SLOT).toBe(0);
      cursor += e.durationMs;
    }
  });

  it("reports a gap as unfilled when its episode has no assignment", () => {
    const res = composeSlotGridEntries(
      [
        { draftId: "p0", mediaId: "a", durationMs: 18 * MIN },
        { draftId: "p1", mediaId: "b", durationMs: SLOT },
      ],
      SLOT,
      new Map(), // nothing assigned
      fillerMap([{ mediaId: "f", packagedDurationMs: SLOT, title: "Bumper" }]),
    );
    expect(res.unfilledGapCount).toBe(1);
    expect(res.unfilledGapMs).toBe(12 * MIN);
    expect(res.fillerMediaIds).toEqual([]);
  });

  it("treats an assigned-but-too-short filler as unfilled", () => {
    const res = composeSlotGridEntries(
      [
        { draftId: "p0", mediaId: "a", durationMs: 18 * MIN },
        { draftId: "p1", mediaId: "b", durationMs: SLOT },
      ],
      SLOT,
      new Map([["p0", "f"]]),
      fillerMap([{ mediaId: "f", packagedDurationMs: 5 * MIN, title: "Short" }]),
    );
    expect(res.unfilledGapCount).toBe(1);
    expect(res.fillerMediaIds).toEqual([]);
  });
});
