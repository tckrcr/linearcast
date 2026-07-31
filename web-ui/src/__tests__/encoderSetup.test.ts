import { describe, expect, it } from "vitest";
import {
  ENCODER_PLATFORM_OPTIONS,
  PRIMARY_ENCODER_DOWNLOADS,
  findDownload,
  platformLabel,
  renderSetupPlan,
} from "../encoderSetup";
import type { EncoderDownloadEntry } from "../types";

const API_KEY = "test-api-key";
const ADMIN_URL = "https://linearcast.example/hls";

describe("renderSetupPlan", () => {
  it("ships a systemd unit for linux that resolves home at runtime", () => {
    const plan = renderSetupPlan("linux-amd64", API_KEY, ADMIN_URL);
    expect(plan.unitFile?.filename).toBe("linearcast-encoder.service");
    // %h is systemd's own home specifier, so one unit file works for any user;
    // a literal path here would silently bind the unit to whoever generated it.
    expect(plan.unitFile?.body).toContain("ExecStart=%h/.linearcast-encoder/linearcast-encoder-linux-amd64 run");
    expect(plan.unitFile?.body).toContain(`Environment=LINEARCAST_ENCODER_API_KEY=${API_KEY}`);
    expect(plan.unitFile?.body).toContain(`Environment=LINEARCAST_ADMIN_URL=${ADMIN_URL}`);
    expect(plan.install).toContain("systemctl --user enable --now linearcast-encoder");
  });

  it("names the arch-specific binary for linux-arm64", () => {
    const plan = renderSetupPlan("linux-arm64", API_KEY, ADMIN_URL);
    expect(plan.unitFile?.body).toContain("linearcast-encoder-linux-arm64 run");
    expect(plan.install).toContain("mv ~/Downloads/linearcast-encoder-linux-arm64");
  });

  it("ships a launchd plist for macOS whose __HOME__ placeholder the install script substitutes", () => {
    const plan = renderSetupPlan("darwin-arm64", API_KEY, ADMIN_URL);
    expect(plan.unitFile?.filename).toBe("com.linearcast.encoder.plist");
    expect(plan.unitFile?.mimeType).toBe("application/xml");
    // launchd expands neither ~ nor $HOME inside a plist, so the placeholder
    // must survive into the file and the install snippet must rewrite it.
    expect(plan.unitFile?.body).toContain("__HOME__/.linearcast-encoder/linearcast-encoder-darwin-arm64");
    expect(plan.install).toContain('sed -i \'\' "s|__HOME__|$HOME|g"');
    expect(plan.install).toContain("launchctl load");
  });

  it("points each mac arch at its own homebrew prefix", () => {
    expect(renderSetupPlan("darwin-arm64", API_KEY, ADMIN_URL).unitFile?.body).toContain("/opt/homebrew/bin");
    expect(renderSetupPlan("darwin-amd64", API_KEY, ADMIN_URL).unitFile?.body).toContain("/usr/local/bin");
  });

  it("clears macOS quarantine so a downloaded binary can run", () => {
    const plan = renderSetupPlan("darwin-arm64", API_KEY, ADMIN_URL);
    expect(plan.install).toContain("xattr -d com.apple.quarantine");
  });

  it("uses batch env vars and no unit file on windows", () => {
    const plan = renderSetupPlan("windows-amd64", API_KEY, ADMIN_URL);
    expect(plan.unitFile).toBeUndefined();
    expect(plan.install).toContain(`set LINEARCAST_ENCODER_API_KEY=${API_KEY}`);
    expect(plan.install).toContain("linearcast-encoder-windows-amd64.exe install");
    expect(plan.manage).toContain("taskkill /IM linearcast-encoder-windows-amd64.exe /F");
  });

  it("offers manage steps for every platform", () => {
    for (const { platform } of ENCODER_PLATFORM_OPTIONS) {
      const plan = renderSetupPlan(platform, API_KEY, ADMIN_URL);
      expect(plan.manage, platform).toBeTruthy();
      expect(plan.install, platform).toBeTruthy();
    }
  });
});

describe("ENCODER_PLATFORM_OPTIONS", () => {
  it("covers every platform with labels agreeing with platformLabel", () => {
    expect(ENCODER_PLATFORM_OPTIONS.map((o) => o.platform)).toEqual([
      "darwin-arm64",
      "darwin-amd64",
      "windows-amd64",
      "linux-amd64",
      "linux-arm64",
    ]);
    for (const opt of ENCODER_PLATFORM_OPTIONS) {
      expect(opt.label).toBe(platformLabel(opt.platform));
    }
  });

  it("keeps the up-front download list a subset of the full platform set", () => {
    const all = ENCODER_PLATFORM_OPTIONS.map((o) => o.platform);
    for (const { platform } of PRIMARY_ENCODER_DOWNLOADS) {
      expect(all).toContain(platform);
    }
  });
});

describe("platformLabel", () => {
  it("falls back to the raw value for a platform it does not know", () => {
    expect(platformLabel("freebsd-amd64")).toBe("freebsd-amd64");
  });
});

describe("findDownload", () => {
  const entries = [
    { platform: "linux-amd64" },
    { platform: "darwin-arm64" },
  ] as EncoderDownloadEntry[];

  it("finds a built platform and returns null for one the server lacks", () => {
    expect(findDownload(entries, "darwin-arm64")).toBe(entries[1]);
    expect(findDownload(entries, "windows-amd64")).toBeNull();
    expect(findDownload([], "linux-amd64")).toBeNull();
  });
});
