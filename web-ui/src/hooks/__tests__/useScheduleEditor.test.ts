import { act, renderHook } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { ChannelNow, ScheduleInsertItem } from "../../types";

const api = vi.hoisted(() => ({
  addChannelMedia: vi.fn(),
  createScheduleBuilderChannel: vi.fn(),
  deleteScheduleEntry: vi.fn(),
  getChannelMedia: vi.fn(),
  getChannelSchedule: vi.fn(),
  getChannelSchedulePreview: vi.fn(),
  getMediaPackageCandidates: vi.fn(),
  getScheduleBuilderCandidates: vi.fn(),
  insertScheduleEntryAfter: vi.fn(),
  insertScheduleEntryBefore: vi.fn(),
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
    expect(onImported).toHaveBeenCalledWith("ch-42");
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
