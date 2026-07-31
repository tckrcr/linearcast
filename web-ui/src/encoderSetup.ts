// Remote-encoder provisioning logic: which platforms exist, which one the
// operator is probably on, and the exact service file plus shell snippets they
// need to install an encoder on it.
//
// This module is deliberately free of React and of DOM writes so the generated
// unit files and scripts can be asserted directly in tests. Handing a generated
// file to the browser as a download is the panel's job, not this module's.
import type { EncoderDownloadEntry } from "./types";

// The platform set is the single source of truth for both the type and the
// selectable options below.
const ENCODER_PLATFORMS = [
  "darwin-arm64",
  "darwin-amd64",
  "windows-amd64",
  "linux-amd64",
  "linux-arm64",
] as const;

export type EncoderPlatform = (typeof ENCODER_PLATFORMS)[number];

export function platformLabel(platform: string): string {
  switch (platform) {
    case "darwin-arm64": return "macOS (Apple Silicon)";
    case "darwin-amd64": return "macOS (Intel)";
    case "windows-amd64": return "Windows";
    case "linux-amd64": return "Linux (x86_64)";
    case "linux-arm64": return "Linux (ARM64)";
    default: return platform;
  }
}

// Every platform, for the full picker in the setup dialog.
export const ENCODER_PLATFORM_OPTIONS: Array<{ platform: EncoderPlatform; label: string }> =
  ENCODER_PLATFORMS.map((platform) => ({ platform, label: platformLabel(platform) }));

// The short list offered up front on the downloads control. Labels are
// deliberately terser than platformLabel's, and it omits the less common
// architectures.
export const PRIMARY_ENCODER_DOWNLOADS: Array<{ platform: EncoderPlatform; label: string }> = [
  { platform: "darwin-arm64", label: "macOS" },
  { platform: "windows-amd64", label: "Windows" },
  { platform: "linux-amd64", label: "Linux x86" },
];

export function findDownload(entries: EncoderDownloadEntry[], platform: EncoderPlatform): EncoderDownloadEntry | null {
  return entries.find((entry) => entry.platform === platform) ?? null;
}

export function defaultPrimaryPlatform(): EncoderPlatform {
  const platform = detectOS();
  if (platform === "windows-amd64") return "windows-amd64";
  if (platform === "darwin-arm64" || platform === "darwin-amd64") return "darwin-arm64";
  return "linux-amd64";
}

export function detectOS(): EncoderPlatform {
  const platform = detectPlatform();
  if (platform === "darwin-arm64" || platform === "darwin-amd64") return platform;
  if (platform === "windows-amd64") return platform;
  if (platform === "linux-arm64" || platform === "linux-amd64") return platform;
  return "linux-amd64";
}

function detectPlatform(): string {
  if (typeof navigator === "undefined") return "";
  const ua = navigator.userAgent.toLowerCase();
  const platform = (navigator.platform || "").toLowerCase();
  if (ua.includes("windows") || platform.includes("win")) return "windows-amd64";
  if (ua.includes("mac") || platform.includes("mac")) {
    // Apple Silicon is the common case on modern Macs. Browsers don't reliably
    // expose arch, so we default to arm64 and let the user pick Intel if needed.
    return "darwin-arm64";
  }
  if (ua.includes("linux") || platform.includes("linux")) {
    if (ua.includes("aarch64") || ua.includes("arm64")) return "linux-arm64";
    return "linux-amd64";
  }
  return "";
}

export type SetupPlan = {
  // Optional unit/plist file the operator installs into a service manager.
  // When set, the dialog offers a download button and shows the file body.
  unitFile?: { filename: string; mimeType: string; body: string };
  // The shell snippet the operator runs to install and start the encoder.
  install: string;
  // Optional follow-up commands (status/logs/uninstall) shown collapsed.
  manage?: string;
};

export function renderSetupPlan(platform: string, apiKey: string, adminUrl: string): SetupPlan {
  if (platform === "windows-amd64") {
    const binary = `linearcast-encoder-windows-amd64.exe`;
    const install = [
      `:: Run from the folder where you saved the .exe (e.g. %USERPROFILE%\\linearcast-encoder).`,
      `set LINEARCAST_ADMIN_URL=${adminUrl}`,
      `set LINEARCAST_ENCODER_API_KEY=${apiKey}`,
      `set LINEARCAST_ENCODER_WORK_DIR=%USERPROFILE%\\linearcast-encoder-work`,
      `${binary} check`,
      `${binary} install`,
    ].join("\n");
    const manage = [
      `:: Start now (also runs at next logon)`,
      `"%APPDATA%\\Microsoft\\Windows\\Start Menu\\Programs\\Startup\\linearcast-encoder.bat"`,
      ``,
      `:: Stop`,
      `taskkill /IM linearcast-encoder-windows-amd64.exe /F`,
      ``,
      `:: Uninstall (removes the Startup script)`,
      `${binary} uninstall`,
    ].join("\n");
    return { install, manage };
  }
  const binary = `linearcast-encoder-${platform}`;
  if (platform.startsWith("darwin")) {
    return {
      unitFile: {
        filename: "com.linearcast.encoder.plist",
        mimeType: "application/xml",
        body: launchdPlist({ binary, apiKey, adminUrl, ffmpegDir: defaultMacFFmpegDir(platform) }),
      },
      install: macInstallScript(binary),
      manage: macManageScript(),
    };
  }
  // linux-amd64 / linux-arm64
  return {
    unitFile: {
      filename: "linearcast-encoder.service",
      mimeType: "text/plain",
      body: systemdUnit({ binary, apiKey, adminUrl }),
    },
    install: linuxInstallScript(binary),
    manage: linuxManageScript(),
  };
}

function systemdUnit({ binary, apiKey, adminUrl }: { binary: string; apiKey: string; adminUrl: string }): string {
  // %h is the systemd specifier for the user's home directory, expanded at
  // runtime by systemd itself — so the same unit file works for any user.
  return [
    `[Unit]`,
    `Description=Linearcast remote encoder`,
    `After=network-online.target`,
    `Wants=network-online.target`,
    ``,
    `[Service]`,
    `Type=simple`,
    `Environment=LINEARCAST_ADMIN_URL=${adminUrl}`,
    `Environment=LINEARCAST_ENCODER_API_KEY=${apiKey}`,
    `Environment=LINEARCAST_ENCODER_WORK_DIR=%h/.linearcast-encoder/work`,
    `ExecStart=%h/.linearcast-encoder/${binary} run`,
    `Restart=on-failure`,
    `RestartSec=10`,
    ``,
    `[Install]`,
    `WantedBy=default.target`,
    ``,
  ].join("\n");
}

function launchdPlist({
  binary,
  apiKey,
  adminUrl,
  ffmpegDir,
}: {
  binary: string;
  apiKey: string;
  adminUrl: string;
  ffmpegDir: string;
}): string {
  // launchd doesn't expand ~ or $HOME inside the plist, so we use the literal
  // placeholder __HOME__ and have the install snippet substitute it for $HOME
  // at install time.
  return [
    `<?xml version="1.0" encoding="UTF-8"?>`,
    `<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">`,
    `<plist version="1.0">`,
    `<dict>`,
    `  <key>Label</key><string>com.linearcast.encoder</string>`,
    `  <key>ProgramArguments</key>`,
    `  <array>`,
    `    <string>__HOME__/.linearcast-encoder/${binary}</string>`,
    `    <string>run</string>`,
    `  </array>`,
    `  <key>EnvironmentVariables</key>`,
    `  <dict>`,
    `    <key>LINEARCAST_ADMIN_URL</key><string>${adminUrl}</string>`,
    `    <key>LINEARCAST_ENCODER_API_KEY</key><string>${apiKey}</string>`,
    `    <key>LINEARCAST_ENCODER_WORK_DIR</key><string>__HOME__/.linearcast-encoder/work</string>`,
    `    <key>LINEARCAST_FFMPEG_DIR</key><string>${ffmpegDir}</string>`,
    `  </dict>`,
    `  <key>KeepAlive</key><true/>`,
    `  <key>RunAtLoad</key><true/>`,
    `  <key>ThrottleInterval</key><integer>30</integer>`,
    `  <key>StandardOutPath</key><string>__HOME__/.linearcast-encoder/encoder.log</string>`,
    `  <key>StandardErrorPath</key><string>__HOME__/.linearcast-encoder/encoder.log</string>`,
    `</dict>`,
    `</plist>`,
    ``,
  ].join("\n");
}

function defaultMacFFmpegDir(platform: string): string {
  return platform === "darwin-amd64" ? "/usr/local/bin" : "/opt/homebrew/bin";
}

function macInstallScript(binary: string): string {
  return [
    `mkdir -p ~/.linearcast-encoder/work`,
    `mv ~/Downloads/${binary} ~/.linearcast-encoder/${binary}`,
    `chmod +x ~/.linearcast-encoder/${binary}`,
    `xattr -d com.apple.quarantine ~/.linearcast-encoder/${binary} 2>/dev/null || true`,
    ``,
    `mv ~/Downloads/com.linearcast.encoder.plist ~/Library/LaunchAgents/`,
    `sed -i '' "s|__HOME__|$HOME|g" ~/Library/LaunchAgents/com.linearcast.encoder.plist`,
    `launchctl load ~/Library/LaunchAgents/com.linearcast.encoder.plist`,
  ].join("\n");
}

function macManageScript(): string {
  return [
    `# Status`,
    `launchctl list | grep linearcast`,
    ``,
    `# Logs`,
    `tail -f ~/.linearcast-encoder/encoder.log`,
    ``,
    `# Stop / uninstall`,
    `launchctl unload ~/Library/LaunchAgents/com.linearcast.encoder.plist`,
    `rm ~/Library/LaunchAgents/com.linearcast.encoder.plist`,
    `rm -rf ~/.linearcast-encoder`,
  ].join("\n");
}

function linuxInstallScript(binary: string): string {
  return [
    `mkdir -p ~/.linearcast-encoder/work`,
    `mv ~/Downloads/${binary} ~/.linearcast-encoder/${binary}`,
    `chmod +x ~/.linearcast-encoder/${binary}`,
    ``,
    `mkdir -p ~/.config/systemd/user`,
    `mv ~/Downloads/linearcast-encoder.service ~/.config/systemd/user/`,
    `systemctl --user daemon-reload`,
    `systemctl --user enable --now linearcast-encoder`,
    ``,
    `# Optional: keep running after you log out`,
    `loginctl enable-linger $USER`,
  ].join("\n");
}

function linuxManageScript(): string {
  return [
    `# Status`,
    `systemctl --user status linearcast-encoder`,
    ``,
    `# Logs`,
    `journalctl --user -u linearcast-encoder -f`,
    ``,
    `# Stop / uninstall`,
    `systemctl --user disable --now linearcast-encoder`,
    `rm ~/.config/systemd/user/linearcast-encoder.service`,
    `rm -rf ~/.linearcast-encoder`,
  ].join("\n");
}
