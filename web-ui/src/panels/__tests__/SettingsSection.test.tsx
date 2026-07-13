import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { SettingsSection } from "../ToolsPanel";
import * as api from "../../api";

vi.mock("../../api", () => ({
  getSchedulerTunables: vi.fn(),
  getEncoderSweeperSettings: vi.fn(),
  getSubtitleSettings: vi.fn(),
  getSpotifyUrl: vi.fn(),
  getPublicServerURL: vi.fn(),
  updateSchedulerTunables: vi.fn(),
  updateEncoderSweeperSettings: vi.fn(),
  updateSubtitleSettings: vi.fn(),
  updatePublicServerURL: vi.fn(),
  saveSpotifyUrl: vi.fn(),
  clearSpotifyUrl: vi.fn(),
  probeUpstreamHLS: vi.fn(),
  describeProbeResult: vi.fn(),
}));

const mockedApi = vi.mocked(api);

// The scalar settings must load with valid values so the unified Save passes
// validation and reaches the Spotify upsert/clear path under test.
beforeEach(() => {
  mockedApi.getSchedulerTunables.mockResolvedValue({ horizonHours: 24, lowWaterHours: 6, tickSeconds: 300 });
  mockedApi.getEncoderSweeperSettings.mockResolvedValue({ sweepIntervalSeconds: 30, maxAttempts: 5 });
  mockedApi.getSubtitleSettings.mockResolvedValue({ subtitleAutoEnable: false, subtitleLanguagePreference: ["eng"] });
  mockedApi.getPublicServerURL.mockResolvedValue({ publicServerUrl: "" });
  mockedApi.updateSchedulerTunables.mockResolvedValue({ horizonHours: 24, lowWaterHours: 6, tickSeconds: 300 });
  mockedApi.updateEncoderSweeperSettings.mockResolvedValue({ sweepIntervalSeconds: 30, maxAttempts: 5 });
  mockedApi.updateSubtitleSettings.mockResolvedValue({ subtitleAutoEnable: false, subtitleLanguagePreference: ["eng"] });
  mockedApi.updatePublicServerURL.mockResolvedValue({ publicServerUrl: "" });
});

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("SettingsSection — Spotify URL row", () => {
  it("upserts a new Spotify URL through the unified Save", async () => {
    mockedApi.getSpotifyUrl.mockResolvedValue({ configured: false });
    mockedApi.saveSpotifyUrl.mockResolvedValue({
      configured: true,
      channelId: "live",
      upstreamHlsUrl: "https://example.com/s.m3u8",
      status: "live",
    });
    const onChannelChanged = vi.fn();

    render(<SettingsSection onChannelChanged={onChannelChanged} />);

    await waitFor(() => expect(mockedApi.getSpotifyUrl).toHaveBeenCalled());

    fireEvent.change(screen.getByPlaceholderText("https://example.com/stream.m3u8"), {
      target: { value: "https://example.com/s.m3u8" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() =>
      expect(mockedApi.saveSpotifyUrl).toHaveBeenCalledWith("https://example.com/s.m3u8"),
    );
    // The unified Save also persists the scalar settings.
    expect(mockedApi.updateSchedulerTunables).toHaveBeenCalled();
    expect(onChannelChanged).toHaveBeenCalled();
    await waitFor(() => expect(screen.getByText("live")).toBeTruthy());
  });

  it("clears the Spotify channel when the field is emptied and confirmed", async () => {
    mockedApi.getSpotifyUrl.mockResolvedValue({
      configured: true,
      channelId: "live",
      upstreamHlsUrl: "https://example.com/s.m3u8",
      status: "down",
    });
    mockedApi.clearSpotifyUrl.mockResolvedValue({ configured: false, deleted: true });
    const confirmSpy = vi.spyOn(window, "confirm").mockReturnValue(true);
    const onChannelChanged = vi.fn();

    render(<SettingsSection onChannelChanged={onChannelChanged} />);

    await waitFor(() =>
      expect((screen.getByPlaceholderText("https://example.com/stream.m3u8") as HTMLInputElement).value).toBe(
        "https://example.com/s.m3u8",
      ),
    );

    fireEvent.change(screen.getByPlaceholderText("https://example.com/stream.m3u8"), {
      target: { value: "" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => expect(mockedApi.clearSpotifyUrl).toHaveBeenCalled());
    expect(mockedApi.saveSpotifyUrl).not.toHaveBeenCalled();
    expect(onChannelChanged).toHaveBeenCalled();
    confirmSpy.mockRestore();
  });

  it("leaves the Spotify channel untouched when the URL is unchanged", async () => {
    mockedApi.getSpotifyUrl.mockResolvedValue({
      configured: true,
      channelId: "live",
      upstreamHlsUrl: "https://example.com/s.m3u8",
      status: "live",
    });
    const onChannelChanged = vi.fn();

    render(<SettingsSection onChannelChanged={onChannelChanged} />);

    await waitFor(() =>
      expect((screen.getByPlaceholderText("https://example.com/stream.m3u8") as HTMLInputElement).value).toBe(
        "https://example.com/s.m3u8",
      ),
    );

    fireEvent.click(screen.getByRole("button", { name: "Save" }));

    await waitFor(() => expect(mockedApi.updateSubtitleSettings).toHaveBeenCalled());
    expect(mockedApi.saveSpotifyUrl).not.toHaveBeenCalled();
    expect(mockedApi.clearSpotifyUrl).not.toHaveBeenCalled();
    expect(onChannelChanged).not.toHaveBeenCalled();
  });
});
