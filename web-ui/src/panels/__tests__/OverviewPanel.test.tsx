import type { ComponentProps } from "react";
import { render, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { OverviewPanel } from "../OverviewPanel";
import type { ChannelNow } from "../../types";

vi.mock("../../api", () => ({
  getDegraded: vi.fn(async () => ({ degraded: false, signals: [] })),
  getEncoders: vi.fn(async () => ({
    encoders: [],
    localWorker: { id: "local", name: "local", status: "online", lastSeenMs: 0, createdAtMs: 0, enabled: true, concurrency: 1 },
    onDemandEncodings: [],
  })),
  getPackageStatusCounts: vi.fn(async () => ({})),
  getSchedulerTunables: vi.fn(async () => ({ horizonHours: 24, lowWaterHours: 6, tickSeconds: 60 })),
}));

function channel(overrides: Partial<ChannelNow> = {}): ChannelNow {
  return {
    id: "c1",
    displayName: "Channel One",
    enabled: true,
    hiddenFromGuide: false,
    ordering: "sequential",
    mediaKind: "video",
    prefillMode: "on_demand",
    status: "playing",
    current: null,
    next: null,
    scheduleCoverageMs: 24 * 3600_000,
    scheduleCoverageHours: 24,
    packageCoverageMs: 0,
    packageCoverageHours: 0,
    packageReadyCount: 0,
    packageProfile: "h264-1080p",
    ...overrides,
  };
}

function renderPanel(overrides: Partial<ComponentProps<typeof OverviewPanel>> = {}) {
  const props: ComponentProps<typeof OverviewPanel> = {
    channels: [channel()],
    loaded: true,
    disabledCount: 0,
    busy: {},
    status: {},
    onSelectChannel: vi.fn(),
    onOpenPanel: vi.fn(),
    onExtend: vi.fn(),
    ...overrides,
  };
  return { props, ...render(<OverviewPanel {...props} />) };
}

describe("OverviewPanel", () => {
  it("reports nothing to do when every channel is playing with runway to spare", async () => {
    renderPanel();
    expect(await screen.findByText(/Nothing needs you/)).toBeTruthy();
  });

  // An on-demand channel encodes as it plays, so zero ready packages is its
  // normal resting state. Alarming on it made every on-demand channel — the
  // default kind — permanently "in need of attention".
  it("does not fault an on-demand channel for having no ready packages", async () => {
    renderPanel({ channels: [channel({ prefillMode: "on_demand", packageReadyCount: 0 })] });
    expect(await screen.findByText(/Nothing needs you/)).toBeTruthy();
    expect(screen.queryByText(/has no encoded programs/)).toBeNull();
  });

  it("faults a pre-encoded channel for having no ready packages", async () => {
    renderPanel({ channels: [channel({ prefillMode: "eager", packageReadyCount: 0 })] });
    expect(await screen.findByText("Channel One has no encoded programs")).toBeTruthy();
  });

  // Coverage sails under low-water between extender ticks by design. Treating
  // the scheduler's own trigger as an operator alarm meant a channel one minute
  // into its normal refill cycle was reported as a fault.
  it("stays quiet about a channel under low-water that the scheduler will refill", async () => {
    renderPanel({ channels: [channel({ scheduleCoverageMs: 5 * 3600_000, scheduleCoverageHours: 5 })] });
    expect(await screen.findByText(/Nothing needs you/)).toBeTruthy();
    expect(screen.queryByText(/runs out in/)).toBeNull();
  });

  it("flags a channel the scheduler has evidently stopped refilling, and extends it on request", async () => {
    const onExtend = vi.fn();
    renderPanel({
      channels: [channel({ scheduleCoverageMs: 20 * 60_000, scheduleCoverageHours: 1 / 3 })],
      onExtend,
    });
    // The message cites the live scheduler setting, not a constant in the UI.
    await waitFor(() => expect(screen.getByText(/extends below 6h and has not/)).toBeTruthy());
    screen.getByRole("button", { name: "Extend 24h" }).click();
    expect(onExtend).toHaveBeenCalledWith("c1", 24);
  });

  // A very short low-water must not put every channel permanently in the red.
  it("keeps the urgent floor below the low-water mark on a short-horizon appliance", async () => {
    const { getSchedulerTunables } = await import("../../api");
    vi.mocked(getSchedulerTunables).mockResolvedValueOnce({
      horizonHours: 1,
      lowWaterHours: 0.5,
      tickSeconds: 60,
    });
    renderPanel({ channels: [channel({ scheduleCoverageMs: 20 * 60_000, scheduleCoverageHours: 1 / 3 })] });
    expect(await screen.findByText(/Nothing needs you/)).toBeTruthy();
  });

  it("treats a schedule gap as more urgent than a short runway", async () => {
    renderPanel({
      channels: [
        channel({ id: "short", displayName: "Short", scheduleCoverageMs: 10 * 60_000, scheduleCoverageHours: 1 / 6 }),
        channel({ id: "gapped", displayName: "Gapped", status: "gap", scheduleCoverageMs: 0, scheduleCoverageHours: 0 }),
      ],
    });
    const list = await screen.findByRole("list", { name: "Needs attention" });
    const rows = within(list).getAllByRole("listitem");
    expect(rows[0].textContent).toContain("Gapped is in a schedule gap");
    expect(rows[1].textContent).toContain("Short runs out in");
  });

  it("shows the last action's result on the row it was triggered from", async () => {
    renderPanel({
      channels: [channel({ status: "gap", scheduleCoverageMs: 0, scheduleCoverageHours: 0 })],
      status: { c1: "inserted 12 entries" },
    });
    expect(await screen.findByText("inserted 12 entries")).toBeTruthy();
    expect(screen.queryByText("No program covers the current time.")).toBeNull();
  });
});
