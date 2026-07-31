import { act, renderHook } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { useStreamProbe } from "../hooks";

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("useStreamProbe", () => {
  it("waits for a real source before probing a manifest", async () => {
    vi.useFakeTimers();
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);

    const { result, rerender } = renderHook(
      ({ source }) => useStreamProbe(source),
      { initialProps: { source: "" } },
    );

    await act(async () => {
      await vi.runOnlyPendingTimersAsync();
    });
    expect(result.current).toEqual({
      status: "checking",
      detail: "loading channels",
    });
    expect(fetchMock).not.toHaveBeenCalled();

    rerender({ source: "/hls/channels/ch/stream.m3u8" });
    expect(result.current).toEqual({
      status: "checking",
      detail: "probing manifest",
    });
  });
});
