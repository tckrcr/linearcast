import { describe, expect, it } from "vitest";
import { isCurrentScheduleEntry, scheduleDurationMs } from "../useScheduleEditor";

describe("scheduleDurationMs", () => {
  it("snaps down to the 6s segment grid", () => {
    expect(scheduleDurationMs(6000)).toBe(6000);
    expect(scheduleDurationMs(11999)).toBe(6000);
    expect(scheduleDurationMs(12000)).toBe(12000);
    expect(scheduleDurationMs(12001)).toBe(12000);
  });

  it("returns 0 for durations shorter than one segment", () => {
    expect(scheduleDurationMs(0)).toBe(0);
    expect(scheduleDurationMs(5999)).toBe(0);
  });

  it("treats null/undefined/non-finite as 0", () => {
    expect(scheduleDurationMs(null)).toBe(0);
    expect(scheduleDurationMs(undefined)).toBe(0);
    expect(scheduleDurationMs(NaN)).toBe(0);
    expect(scheduleDurationMs(Infinity)).toBe(0);
  });

  it("clamps negative values to 0", () => {
    expect(scheduleDurationMs(-6000)).toBe(0);
  });
});

describe("isCurrentScheduleEntry", () => {
  const entry = { startMs: 1000, endMs: 2000 };

  it("is true when now is in [start, end)", () => {
    expect(isCurrentScheduleEntry(entry, 1000)).toBe(true);
    expect(isCurrentScheduleEntry(entry, 1500)).toBe(true);
    expect(isCurrentScheduleEntry(entry, 1999)).toBe(true);
  });

  it("is false at the exact end (half-open interval)", () => {
    expect(isCurrentScheduleEntry(entry, 2000)).toBe(false);
  });

  it("is false outside the window", () => {
    expect(isCurrentScheduleEntry(entry, 999)).toBe(false);
    expect(isCurrentScheduleEntry(entry, 2001)).toBe(false);
  });
});
