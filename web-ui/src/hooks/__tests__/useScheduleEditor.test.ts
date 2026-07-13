import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ChannelNow, ScheduleInsertItem } from "../../types";

const api = vi.hoisted(() => ({
  addChannelMedia: vi.fn(),
  createScheduleBuilderChannel: vi.fn(),
  deleteScheduleEntry: vi.fn(),
  fillScheduleGap: vi.fn(),
  getChannelFillerAssets: vi.fn(),
  getChannelMedia: vi.fn(),
  getChannelSchedule: vi.fn(),
  getChannelSchedulePreview: vi.fn(),
  getMediaPackageCandidates: vi.fn(),
  getScheduleBuilderCandidates: vi.fn(),
  getScheduleBuilderFillerCandidates: vi.fn(),
  insertScheduleEntryAfter: vi.fn(),
  insertScheduleEntryBefore: vi.fn(),
  recomposeSlotGridSchedule: vi.fn(),
  saveScheduleWindowOrdered: vi.fn(),
  upsertScheduleEntry: vi.fn(),
}));

vi.mock("../../api", () => api);

import { useScheduleEditor } from "../useScheduleEditor";

function insertItem(overrides: Partial<ScheduleInsertItem> = {}): ScheduleInsertItem {
  return {
    mediaId: "m1",
    title: "One",
    path: "/m/1.mp4",
    durationMs: 60000,
    packagedDurationMs: 60000,
    packageReady: true,
    ...overrides,
  };
}

function draftConfig(onImported = vi.fn()) {
  return {
    packageProfile: "default",
    displayName: "My Draft",
    onImported,
  };
}

function channel(overrides: Partial<ChannelNow> = {}): ChannelNow {
  return {
    id: "ch",
    displayName: "Channel",
    enabled: true,
    hiddenFromGuide: false,
    ordering: "manual",
    mediaKind: "video",
    status: "playing",
    current: null,
    next: null,
    scheduleCoverageMs: 0,
    scheduleCoverageHours: 0,
    packageCoverageMs: 0,
    packageCoverageHours: 0,
    packageReadyCount: 0,
    packageProfile: "default",
    ...overrides,
  };
}

beforeEach(() => {
  vi.clearAllMocks();
});

afterEach(() => {
  vi.useRealTimers();
});

describe("useScheduleEditor — draft-mode invariants", () => {
  it("does not call getChannelSchedule on mount when channel is null", () => {
    renderHook(() => useScheduleEditor(null, draftConfig()));
    expect(api.getChannelSchedule).not.toHaveBeenCalled();
  });

  it("starts in edit mode and reports isDraftMode", () => {
    const { result } = renderHook(() => useScheduleEditor(null, draftConfig()));
    expect(result.current.isDraftMode).toBe(true);
    expect(result.current.scheduleEditMode).toBe(true);
  });

  it("saveScheduleEdit is a no-op (no live save call)", async () => {
    const { result } = renderHook(() => useScheduleEditor(null, draftConfig()));
    await act(async () => {
      await result.current.saveScheduleEdit();
    });
    expect(api.saveScheduleWindowOrdered).not.toHaveBeenCalled();
  });

  it("does not auto-refresh on a timer in draft mode", () => {
    vi.useFakeTimers();
    renderHook(() => useScheduleEditor(null, draftConfig()));
    vi.advanceTimersByTime(120_000);
    expect(api.getChannelSchedule).not.toHaveBeenCalled();
  });

  it("loadMedia uses getScheduleBuilderCandidates, not getChannelMedia", async () => {
    api.getScheduleBuilderCandidates.mockResolvedValue({ media: [] });
    const { result } = renderHook(() => useScheduleEditor(null, draftConfig()));
    await act(async () => {
      await result.current.loadMedia(true);
    });
    expect(api.getScheduleBuilderCandidates).toHaveBeenCalledWith("default", undefined, "all");
    expect(api.getChannelMedia).not.toHaveBeenCalled();
  });
});

describe("useScheduleEditor — importDraftChannel", () => {
  it("calls createScheduleBuilderChannel then onImported", async () => {
    const order: string[] = [];
    api.createScheduleBuilderChannel.mockImplementation(async () => {
      order.push("createScheduleBuilderChannel");
      return { channelID: "ch-42", scheduleEntries: 1, queued: ["m1"] };
    });
    const onImported = vi.fn(() => order.push("onImported"));
    const cfg = draftConfig(onImported);

    const { result } = renderHook(() => useScheduleEditor(null, cfg));
    act(() => {
      result.current.appendDraftEntry(insertItem());
    });
    await act(async () => {
      await result.current.importDraftChannel();
    });

    expect(order).toEqual(["createScheduleBuilderChannel", "onImported"]);
    expect(api.createScheduleBuilderChannel).toHaveBeenCalledWith({
      displayName: "My Draft",
      packageProfile: "default",
      mediaIds: ["m1"],
    });
    expect(onImported).toHaveBeenCalledWith("ch-42", { scheduleMode: undefined });
  });

  it("treats a create response without queue details as success", async () => {
    const onImported = vi.fn();
    api.createScheduleBuilderChannel.mockResolvedValue({
      channelID: "ch-on-demand",
      displayName: "My Draft",
      created: true,
      syncedMedia: 1,
      scheduleEntries: 1,
      profile: "default",
    });
    const cfg = { ...draftConfig(onImported), prefillMode: "on_demand" as const };

    const { result } = renderHook(() => useScheduleEditor(null, cfg));
    act(() => {
      result.current.appendDraftEntry(insertItem());
    });
    await act(async () => {
      await result.current.importDraftChannel();
    });

    expect(result.current.scheduleError).toBe("");
    expect(result.current.scheduleNotice).toContain("created ch-on-demand");
    expect(onImported).toHaveBeenCalledWith("ch-on-demand", { scheduleMode: undefined });
  });

  it("does nothing when the draft is empty", async () => {
    const { result } = renderHook(() => useScheduleEditor(null, draftConfig()));
    await act(async () => {
      await result.current.importDraftChannel();
    });
    expect(api.createScheduleBuilderChannel).not.toHaveBeenCalled();
  });

  it("surfaces an error when displayName is blank", async () => {
    const cfg = { ...draftConfig(), displayName: "   " };
    const { result } = renderHook(() => useScheduleEditor(null, cfg));
    act(() => {
      result.current.appendDraftEntry(insertItem());
    });
    await act(async () => {
      await result.current.importDraftChannel();
    });
    expect(api.createScheduleBuilderChannel).not.toHaveBeenCalled();
    expect(result.current.scheduleError).toMatch(/display name/i);
  });

  it("dedups mediaIds passed to createScheduleBuilderChannel", async () => {
    api.createScheduleBuilderChannel.mockResolvedValue({ channelID: "ch-1", scheduleEntries: 0, queued: [] });
    const { result } = renderHook(() => useScheduleEditor(null, draftConfig()));
    act(() => {
      result.current.appendDraftEntry(insertItem({ mediaId: "m1", path: "/a" }));
      result.current.appendDraftEntry(insertItem({ mediaId: "m1", path: "/b" }));
    });
    await act(async () => {
      await result.current.importDraftChannel();
    });
    const call = api.createScheduleBuilderChannel.mock.calls[0]?.[0] as { mediaIds: string[] };
    expect(call.mediaIds).toEqual(["m1"]);
  });

  it("passes slot-grid draft timing when configured", async () => {
    const onImported = vi.fn();
    api.createScheduleBuilderChannel.mockResolvedValue({ channelID: "ch-1", scheduleEntries: 0, queued: [] });
    const cfg = { ...draftConfig(onImported), scheduleMode: "slot_grid", slotDurationMs: 30 * 60 * 1000 };
    const { result } = renderHook(() => useScheduleEditor(null, cfg));
    act(() => {
      result.current.appendDraftEntry(insertItem());
    });
    await act(async () => {
      await result.current.importDraftChannel();
    });
    expect(api.createScheduleBuilderChannel).toHaveBeenCalledWith({
      displayName: "My Draft",
      packageProfile: "default",
      mediaIds: ["m1"],
      scheduleMode: "slot_grid",
      slotDurationMs: 30 * 60 * 1000,
    });
    expect(onImported).toHaveBeenCalledWith("ch-1", { scheduleMode: "slot_grid" });
  });
});

describe("useScheduleEditor — slot-grid gap fill", () => {
  it("re-syncs the slot-grid draft after a gap fill so the filled gap stops showing as open", async () => {
    const entryOne = { entryId: "e1", mediaId: "m1", title: "One", path: "/m/1", startMs: 0, endMs: 18000, durationMs: 18000 };
    const entryTwo = { entryId: "e2", mediaId: "m2", title: "Two", path: "/m/2", startMs: 60000, endMs: 78000, durationMs: 18000 };
    const fillerEntry = { entryId: "f1", mediaId: "bumper", title: "Bumper", path: "/m/b", startMs: 18000, endMs: 60000, durationMs: 42000 };
    const initial = { channelID: "ch", fromMs: 0, toMs: 3600000, entries: [entryOne, entryTwo] };
    const filled = { ...initial, entries: [entryOne, fillerEntry, entryTwo] };

    api.getChannelSchedule.mockResolvedValueOnce(initial).mockResolvedValue(filled);
    api.getChannelMedia.mockResolvedValue({ media: [] });
    api.getChannelFillerAssets.mockResolvedValue({ assets: [{ mediaId: "bumper", enabled: true, channelEnabled: true }] });
    api.getMediaPackageCandidates.mockResolvedValue({
      profile: "default",
      media: [{ mediaId: "bumper", title: "Bumper", path: "/m/b", packageStatus: "ready", packagedDurationMs: 90000 }],
    });
    api.getScheduleBuilderFillerCandidates.mockResolvedValue({ profile: "default", assets: [] });
    api.fillScheduleGap.mockResolvedValue({ startMs: 18000, mediaId: "bumper" });

    const { result } = renderHook(() => useScheduleEditor(channel({ scheduleMode: "slot_grid" })));
    // Initial schedule fetch on mount.
    await act(async () => { await Promise.resolve(); });
    // Load the filler into the picker, then enter slot-grid edit mode.
    await act(async () => { await result.current.loadMedia(true); });
    act(() => { result.current.beginScheduleEdit(); });
    expect(result.current.scheduleDraft.map((e) => e.mediaId)).toEqual(["m1", "m2"]);

    await act(async () => { await result.current.fillGapWithMediaKey("bumper", 18000); });

    expect(api.fillScheduleGap).toHaveBeenCalledWith("ch", "bumper", 18000, 0, "sequential");
    // The draft now reflects the filled gap, so the timeline no longer offers it.
    expect(result.current.scheduleDraft.map((e) => e.mediaId)).toEqual(["m1", "bumper", "m2"]);
  });

  it("recomposeSlotGrid calls the recompose endpoint and refreshes the schedule", async () => {
    const entryOne = { entryId: "e1", mediaId: "m1", title: "One", path: "/m/1", startMs: 0, endMs: 18000, durationMs: 18000 };
    const initial = { channelID: "ch", fromMs: 0, toMs: 3600000, entries: [entryOne] };
    api.getChannelSchedule.mockResolvedValue(initial);
    api.recomposeSlotGridSchedule.mockResolvedValue({
      channelID: "ch", fromMs: 0, cleared: 0, inserted: 4, lastEndMs: 7200000, gappy: false,
    });

    const { result } = renderHook(() => useScheduleEditor(channel({ scheduleMode: "slot_grid" })));
    await act(async () => { await Promise.resolve(); });
    api.getChannelSchedule.mockClear();

    await act(async () => { await result.current.recomposeSlotGrid(); });

    expect(api.recomposeSlotGridSchedule).toHaveBeenCalledWith("ch");
    // The recompose refreshes the schedule from the server.
    expect(api.getChannelSchedule).toHaveBeenCalled();
  });
});

describe("useScheduleEditor — appendDraftEntries dedup and undo", () => {
  it("dedupes incoming items by mediaId against existing draft", () => {
    const { result } = renderHook(() => useScheduleEditor(null, draftConfig()));
    let added = 0;
    act(() => {
      added = result.current.appendDraftEntries([
        insertItem({ mediaId: "m1", path: "/a" }),
        insertItem({ mediaId: "m2", path: "/b" }),
        insertItem({ mediaId: "m1", path: "/c" }), // dup by mediaId
      ]);
    });
    expect(added).toBe(2);
    expect(result.current.scheduleDraft.map((e) => e.mediaId)).toEqual(["m1", "m2"]);
  });

  it("dedupes by path even when mediaId differs", () => {
    const { result } = renderHook(() => useScheduleEditor(null, draftConfig()));
    act(() => {
      result.current.appendDraftEntries([insertItem({ mediaId: "m1", path: "/shared" })]);
    });
    let added = 0;
    act(() => {
      added = result.current.appendDraftEntries([
        insertItem({ mediaId: "m2", path: "/shared" }),
      ]);
    });
    expect(added).toBe(0);
    expect(result.current.scheduleDraft).toHaveLength(1);
  });

  it("skips items whose duration snaps to 0", () => {
    const { result } = renderHook(() => useScheduleEditor(null, draftConfig()));
    let added = 0;
    act(() => {
      added = result.current.appendDraftEntries([
        insertItem({ mediaId: "tiny", durationMs: 100, packagedDurationMs: 100 }),
      ]);
    });
    expect(added).toBe(0);
    expect(result.current.scheduleDraft).toHaveLength(0);
  });

  it("undoScheduleDraftChange restores the prior draft", () => {
    const { result } = renderHook(() => useScheduleEditor(null, draftConfig()));
    act(() => {
      result.current.appendDraftEntries([insertItem({ mediaId: "m1" })]);
    });
    expect(result.current.scheduleDraft).toHaveLength(1);
    expect(result.current.canUndoScheduleDraft).toBe(true);

    act(() => {
      result.current.appendDraftEntries([insertItem({ mediaId: "m2", path: "/m/2" })]);
    });
    expect(result.current.scheduleDraft).toHaveLength(2);

    act(() => {
      result.current.undoScheduleDraftChange();
    });
    expect(result.current.scheduleDraft.map((e) => e.mediaId)).toEqual(["m1"]);
    expect(result.current.canUndoScheduleDraft).toBe(false);
  });
});
