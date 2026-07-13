import { describe, expect, it } from "vitest";
import { advanceClock, fallbackNowMs, resolveLiveSlot } from "../playbackClock";
import type { MediaWindow } from "../types";

function win(startMs: number, endMs: number): MediaWindow {
  return { mediaID: "m", startMs, endMs, durationMs: endMs - startMs };
}

describe("fallbackNowMs", () => {
  it("skew-corrects wall clock against the poll anchor", () => {
    // serverNow 10_000 was captured when the local clock read 1_000, i.e. the
    // server ran 9_000ms ahead of local; at local 1_200, on-screen ≈ 10_200.
    expect(fallbackNowMs({ serverNowMs: 10_000, localAtMs: 1_000 }, 1_200)).toBe(10_200);
  });

  it("uses raw wall clock with no anchor", () => {
    expect(fallbackNowMs(null, 4_242)).toBe(4_242);
  });
});

describe("advanceClock", () => {
  it("prefers a trusted playhead date and records it", () => {
    const r = advanceClock(null, 5_000, { serverNowMs: 9_000, localAtMs: 0 }, 8_000);
    expect(r.nowMs).toBe(5_000);
    expect(r.trusted).toEqual({ playheadMs: 5_000, atWallMs: 8_000 });
  });

  it("extrapolates at 1x across a gap in playhead data", () => {
    const prev = { playheadMs: 5_000, atWallMs: 8_000 };
    const r = advanceClock(prev, null, null, 8_600);
    expect(r.nowMs).toBe(5_600); // 5_000 + (8_600 - 8_000)
    expect(r.trusted).toBe(prev);
  });

  it("falls back to the anchor before any trusted reading", () => {
    const r = advanceClock(null, null, { serverNowMs: 10_000, localAtMs: 1_000 }, 1_200);
    expect(r.nowMs).toBe(10_200);
    expect(r.trusted).toBeNull();
  });
});

describe("resolveLiveSlot", () => {
  it("counts the current program down against the on-screen clock", () => {
    const cur = win(0, 60_000);
    const nxt = win(60_000, 120_000);
    const slot = resolveLiveSlot(cur, nxt, 20_000);
    expect(slot.now).toBe(cur);
    expect(slot.next).toBe(nxt);
    expect(slot.remainingMs).toBe(40_000);
    expect(slot.rolledPast).toBe(false);
  });

  it("rolls to next once the playhead crosses the boundary", () => {
    const cur = win(0, 60_000);
    const nxt = win(60_000, 120_000);
    const slot = resolveLiveSlot(cur, nxt, 65_000);
    expect(slot.now).toBe(nxt);
    expect(slot.next).toBeNull();
    expect(slot.remainingMs).toBe(55_000);
    expect(slot.rolledPast).toBe(true);
  });

  it("clamps remaining at zero past a boundary with no known next", () => {
    const cur = win(0, 60_000);
    const slot = resolveLiveSlot(cur, null, 65_000);
    expect(slot.now).toBe(cur);
    expect(slot.remainingMs).toBe(0);
    expect(slot.rolledPast).toBe(true);
  });

  it("has nothing to count down without a current program", () => {
    const nxt = win(60_000, 120_000);
    const slot = resolveLiveSlot(null, nxt, 1_000);
    expect(slot.now).toBeNull();
    expect(slot.next).toBe(nxt);
    expect(slot.remainingMs).toBeNull();
    expect(slot.rolledPast).toBe(false);
  });
});
