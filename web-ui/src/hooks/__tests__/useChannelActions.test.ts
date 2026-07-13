import { describe, expect, it } from "vitest";
import { profilesForMediaKind } from "../useChannelActions";
import type { PackageProfile } from "../../types";

function profile(name: string, mediaKind: string, tags: string[] = []): PackageProfile {
  return {
    name,
    label: name,
    description: "",
    mediaKind,
    tags,
    video: { mode: "transcode" },
    audio: { mode: "transcode" },
  };
}

describe("profilesForMediaKind", () => {
  it("keeps video profiles even when they are tagged for ABR or HDR", () => {
    const profiles = ["h264-1080p-8mbps", "hevc-copy-source", "music-aac-720p"];
    const details = {
      "h264-1080p-8mbps": profile("h264-1080p-8mbps", "video", ["default"]),
      "hevc-copy-source": profile("hevc-copy-source", "video", ["abr", "hdr"]),
      "music-aac-720p": profile("music-aac-720p", "music"),
    };

    expect(profilesForMediaKind(profiles, details, "video")).toEqual([
      "h264-1080p-8mbps",
      "hevc-copy-source",
    ]);
  });
});
