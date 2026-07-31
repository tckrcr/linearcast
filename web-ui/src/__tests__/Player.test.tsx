import { createRef, type ComponentProps } from "react";
import type Hls from "hls.js";
import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { Player } from "../Player";

vi.mock("../hooks/useHlsPlayer", () => ({
  useHlsPlayer: vi.fn(),
}));

vi.mock("../hooks/usePlaybackStats", () => ({
  usePlaybackStats: vi.fn(),
}));

function renderPlayer(overrides: Partial<ComponentProps<typeof Player>> = {}) {
  const props: ComponentProps<typeof Player> = {
    source: "",
    autoPlay: true,
    muted: true,
    abrMode: "best",
    abrAvailable: false,
    controlsVisible: true,
    onMutedChange: vi.fn(),
    onAbrModeChange: vi.fn(),
    probe: { status: "checking", detail: "loading channels" },
    activeSource: null,
    nowSlot: {
      now: null,
      next: null,
      remainingMs: null,
      rolledPast: false,
    },
    sourcesLoaded: false,
    hasSources: false,
    onStats: vi.fn(),
    videoRef: createRef<HTMLVideoElement>(),
    hlsRef: createRef<Hls>(),
    ...overrides,
  };
  return render(<Player {...props} />);
}

describe("Player initial channel state", () => {
  it("shows tuning while channel metadata is still loading", () => {
    renderPlayer();

    expect(screen.getByText("Tuning in…")).toBeTruthy();
    expect(screen.getByText("loading channels")).toBeTruthy();
    expect(screen.queryByText("No channels configured")).toBeNull();
  });

  it("shows the empty state only after sources load without channels", () => {
    renderPlayer({ sourcesLoaded: true });

    expect(screen.getByText("No channels configured")).toBeTruthy();
    expect(screen.queryByText("Tuning in…")).toBeNull();
  });
});
