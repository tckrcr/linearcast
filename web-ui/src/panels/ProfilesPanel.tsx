import { Fragment, useCallback, useEffect, useState } from "react";
import {
  deletePackageProfile,
  getMediaPackageProfileList,
  setDefaultPackagedProfile,
  upsertPackageProfile,
} from "../api";
import type { PackageProfile } from "../types";
import styles from "./ProfilesPanel.module.css";

type EncoderChoice = "cpu" | "apple" | "nvidia" | "copy";
type ResolutionChoice = "source" | "1080" | "720" | "480";
type QualityChoice = "good" | "better" | "best";

type SimpleProfileDraft = {
  encoder: EncoderChoice;
  resolution: ResolutionChoice;
  quality: QualityChoice;
};

type ProfileSettings = Pick<PackageProfile, "video" | "audio">;

type OverrideKey =
  | "crf"
  | "videoQuality"
  | "preset"
  | "videoBitrate"
  | "videoMaxBitrate"
  | "scaleHeight"
  | "h264Profile";

type OverrideState = Record<OverrideKey, { enabled: boolean; value: string }>;

const DEFAULT_SIMPLE_DRAFT: SimpleProfileDraft = {
  encoder: "cpu",
  resolution: "1080",
  quality: "better",
};

const ENCODER_OPTIONS: Array<{ value: EncoderChoice; label: string }> = [
  { value: "cpu", label: "CPU" },
  { value: "apple", label: "Apple hardware" },
  { value: "nvidia", label: "NVIDIA hardware" },
  { value: "copy", label: "Copy video" },
];

const RESOLUTION_OPTIONS: Array<{ value: ResolutionChoice; label: string }> = [
  { value: "source", label: "Source" },
  { value: "1080", label: "1080p" },
  { value: "720", label: "720p" },
  { value: "480", label: "480p" },
];

const QUALITY_OPTIONS: Array<{ value: QualityChoice; label: string }> = [
  { value: "good", label: "Good" },
  { value: "better", label: "Better" },
  { value: "best", label: "Best" },
];

const X264_CRF: Record<QualityChoice, number> = {
  good: 23,
  better: 20,
  best: 18,
};

const VIDEOTOOLBOX_QUALITY: Record<QualityChoice, number> = {
  good: 55,
  better: 65,
  best: 75,
};

const QUALITY_BITRATES: Record<ResolutionChoice, Record<QualityChoice, string>> = {
  source: { good: "5000k", better: "8000k", best: "12000k" },
  "1080": { good: "5000k", better: "8000k", best: "12000k" },
  "720": { good: "3000k", better: "5000k", best: "7500k" },
  "480": { good: "1500k", better: "2500k", best: "4000k" },
};

const OVERRIDE_META: Record<OverrideKey, { label: string; placeholder?: string; type?: string }> = {
  crf: { label: "CRF", type: "number", placeholder: "23" },
  videoQuality: { label: "VideoToolbox quality (Q)", type: "number", placeholder: "65" },
  preset: { label: "Preset", placeholder: "veryfast, medium, p4" },
  videoBitrate: { label: "Video bitrate", placeholder: "8000k" },
  videoMaxBitrate: { label: "Max bitrate", placeholder: "12000k" },
  scaleHeight: { label: "Scale height", type: "number", placeholder: "1080" },
  h264Profile: { label: "H.264 profile", placeholder: "main, high, baseline" },
};

function applicableOverrides(encoder: EncoderChoice): OverrideKey[] {
  switch (encoder) {
    case "cpu":
      return ["crf", "preset", "scaleHeight", "h264Profile"];
    case "apple":
      return ["videoQuality", "scaleHeight", "h264Profile"];
    case "nvidia":
      return ["preset", "videoBitrate", "videoMaxBitrate", "scaleHeight", "h264Profile"];
    case "copy":
      return [];
  }
}

function emptyOverrideState(): OverrideState {
  return {
    crf: { enabled: false, value: "" },
    videoQuality: { enabled: false, value: "" },
    preset: { enabled: false, value: "" },
    videoBitrate: { enabled: false, value: "" },
    videoMaxBitrate: { enabled: false, value: "" },
    scaleHeight: { enabled: false, value: "" },
    h264Profile: { enabled: false, value: "" },
  };
}

export function ProfilesPanel() {
  const [profiles, setProfiles] = useState<PackageProfile[]>([]);
  const [defaultProfile, setDefaultProfile] = useState("");
  const [loading, setLoading] = useState(true);
  const [status, setStatus] = useState("");
  const [editing, setEditing] = useState<PackageProfile | null>(null);
  const [cloning, setCloning] = useState<PackageProfile | null>(null);
  const [showCreate, setShowCreate] = useState(false);
  const [expanded, setExpanded] = useState<string | null>(null);
  const [menuOpen, setMenuOpen] = useState<string | null>(null);

  useEffect(() => {
    if (!menuOpen) return;
    const handler = (e: MouseEvent) => {
      const target = e.target as Element | null;
      if (!target?.closest(".profile-actions")) setMenuOpen(null);
    };
    document.addEventListener("click", handler);
    return () => document.removeEventListener("click", handler);
  }, [menuOpen]);

  const loadProfiles = useCallback(() => {
    setLoading(true);
    getMediaPackageProfileList()
      .then((data) => {
        setProfiles(data.profileDetails);
        setDefaultProfile(data.defaultProfile);
        setStatus("");
      })
      .catch((err) => {
        setStatus(err instanceof Error ? err.message : String(err));
      })
      .finally(() => setLoading(false));
  }, []);

  async function handleSetDefault(profile: PackageProfile) {
    try {
      setStatus(`Setting ${profile.name} as default…`);
      await setDefaultPackagedProfile(profile.name);
      setStatus(`Default is now ${profile.name}. Restart linearcast + extender for the change to take effect.`);
      loadProfiles();
    } catch (err) {
      setStatus(err instanceof Error ? err.message : String(err));
    }
  }

  useEffect(() => loadProfiles(), [loadProfiles]);

  async function handleSave(profile: PackageProfile) {
    try {
      setStatus(`Saving ${profile.name}...`);
      await upsertPackageProfile(profile);
      setStatus(`Saved ${profile.name}`);
      setEditing(null);
      setCloning(null);
      setShowCreate(false);
      loadProfiles();
    } catch (err) {
      setStatus(err instanceof Error ? err.message : String(err));
    }
  }

  async function handleDelete(profile: PackageProfile) {
    const refs = profile.references;
    const hasReferences = !!refs && (refs.mediaPackages > 0 || refs.channels > 0 || refs.scheduleEntries > 0);
    const action = profile.isBuiltin || hasReferences ? "Disable" : "Delete";
    if (!confirm(`${action} profile "${profile.name}"?`)) return;
    try {
      setStatus(`${action === "Disable" ? "Disabling" : "Deleting"} ${profile.name}...`);
      const result = await deletePackageProfile(profile.name);
      setStatus(result.disabled ? `Disabled ${profile.name}` : `Deleted ${profile.name}`);
      loadProfiles();
    } catch (err) {
      setStatus(err instanceof Error ? err.message : String(err));
    }
  }

  const abrProfiles = abrGroupProfiles(profiles);
  const ladderGroups = buildLadderGroups(profiles);
  const visibleProfiles = profiles.filter((p) => !isABRProfile(p));

  return (
    <div className="admin-panel profiles-panel">
      <section className="admin-panel-section">
        <div className={styles["profiles-panel-header"]}>
          <h2>Package Profiles</h2>
          <div className={styles["profiles-panel-actions"]}>
            <button type="button" disabled={loading} onClick={() => loadProfiles()}>
              {loading ? "Refreshing" : "Refresh"}
            </button>
            <button type="button" onClick={() => { setEditing(null); setCloning(null); setShowCreate(true); }}>
              Create Profile
            </button>
          </div>
        </div>

        {status && <p className="channel-status-msg muted">{status}</p>}

        <div className={styles["profiles-list"]}>
          <div className={styles["profiles-list-header"]} role="row">
            <span>Profile</span>
            <span>Encoder</span>
            <span>Output</span>
            <span>Audio</span>
            <span>Type</span>
            <span>Status</span>
            <span>Actions</span>
          </div>
          {abrProfiles.length > 0 && (
            <Fragment key="abr-ladder">
              <div className={`${styles["profiles-list-row"]}${expanded === "abr-ladder" ? " is-expanded" : ""}`} role="row">
                <div className={styles["profile-identity"]}>
                  <button
                    type="button"
                    className={styles["profile-identity-toggle"]}
                    onClick={() => setExpanded(expanded === "abr-ladder" ? null : "abr-ladder")}
                    aria-expanded={expanded === "abr-ladder"}
                  >
                    <span className={styles["profile-identity-chevron"]}>{expanded === "abr-ladder" ? "▾" : "▸"}</span>
                    <span className={styles["profile-identity-text"]}>
                      <span className={styles["profile-identity-label"]}>Adaptive bitrate</span>
                      {ladderGroups.map((g) => g.profiles.length > 0 && (
                        <code key={g.label} className={styles["profile-identity-name"]} style={{ marginRight: "0.5rem" }}>
                          {g.label} ({g.profiles.length})
                        </code>
                      ))}
                    </span>
                  </button>
                </div>
                <div><span className={`${styles["encoder-badge"]} encoder-cpu`}>ABR</span></div>
                <div>{formatABROutput(abrProfiles)}</div>
                <div>{formatABRAudio(abrProfiles)}</div>
                <div><span className={styles["profile-type"]}>Built-in</span></div>
                <div><span className={styles["profile-status"]}>{abrProfiles.some((p) => p.disabled) ? "Partial" : "Active"}</span></div>
                <div className={styles["profile-actions"]} />
              </div>
              {expanded === "abr-ladder" && (
                <div className={styles["profile-detail-row"]} role="row">
                  <ABRProfileDetail profiles={abrProfiles} ladderGroups={ladderGroups} />
                </div>
              )}
            </Fragment>
          )}
          {visibleProfiles.map((p) => {
            const isDefault = p.name === defaultProfile;
            const isExpanded = expanded === p.name;
            return (
              <Fragment key={p.name}>
                <div className={`${styles["profiles-list-row"]}${p.isBuiltin ? " is-builtin" : ""}${p.disabled ? " is-disabled" : ""}${isDefault ? " is-default" : ""}${isExpanded ? " is-expanded" : ""}`} role="row">
                  <div className={styles["profile-identity"]}>
                    <button
                      type="button"
                      className={styles["profile-identity-toggle"]}
                      onClick={() => setExpanded(isExpanded ? null : p.name)}
                      aria-expanded={isExpanded}
                    >
                      <span className={styles["profile-identity-chevron"]}>{isExpanded ? "▾" : "▸"}</span>
                      <span className={styles["profile-identity-text"]}>
                        <span className={styles["profile-identity-label"]}>{p.label}</span>
                        <code className={styles["profile-identity-name"]}>{p.name}</code>
                      </span>
                      {isDefault && <span className={styles["profile-default-badge"]} title="Default profile for new channels">★ default</span>}
                    </button>
                  </div>
                  <div><span className={`${styles["encoder-badge"]} encoder-${encoderTierKey(p)}`}>{formatEncoderTier(p)}</span></div>
                  <div>{formatOutput(p)}</div>
                  <div>{formatAudioCell(p)}</div>
                  <div><span className={styles["profile-type"]}>{p.isBuiltin ? "Built-in" : "Custom"}</span></div>
                  <div><span className={styles["profile-status"]}>{p.disabled ? "Disabled" : "Active"}</span></div>
                  <div className={styles["profile-actions"]}>
                    <button
                      type="button"
                      className={styles["profile-actions-toggle"]}
                      aria-haspopup="menu"
                      aria-expanded={menuOpen === p.name}
                      title="Actions"
                      onClick={(e) => { e.stopPropagation(); setMenuOpen(menuOpen === p.name ? null : p.name); }}
                    >
                      ⋯
                    </button>
                    {menuOpen === p.name && (
                      <div className={styles["profile-actions-menu"]} role="menu">
                        {!p.disabled && !isDefault && (
                          <button
                            type="button"
                            role="menuitem"
                            onClick={() => { setMenuOpen(null); void handleSetDefault(p); }}
                          >
                            Make default
                          </button>
                        )}
                        {!p.disabled && !p.isBuiltin && (
                          <button
                            type="button"
                            role="menuitem"
                            onClick={() => { setMenuOpen(null); setEditing(p); setCloning(null); setShowCreate(false); }}
                          >
                            Change
                          </button>
                        )}
                        <button
                          type="button"
                          role="menuitem"
                          onClick={() => { setMenuOpen(null); setCloning(p); setEditing(null); setShowCreate(false); }}
                        >
                          Clone
                        </button>
                        {!p.disabled && (
                          <button
                            type="button"
                            role="menuitem"
                            className="danger"
                            onClick={() => { setMenuOpen(null); void handleDelete(p); }}
                          >
                            {p.isBuiltin || profileHasReferences(p) ? "Disable" : "Delete"}
                          </button>
                        )}
                        {p.disabled && (
                          <span className={styles["profile-actions-menu-empty"]}>Read-only</span>
                        )}
                      </div>
                    )}
                  </div>
                </div>
                {isExpanded && (
                  <div className={styles["profile-detail-row"]} role="row">
                    <ProfileDetail profile={p} />
                  </div>
                )}
              </Fragment>
            );
          })}
          {profiles.length === 0 && !loading && (
            <div className="profiles-empty muted">No profiles found</div>
          )}
        </div>
      </section>

      {(showCreate || editing || cloning) && (
        <section className="admin-panel-section">
          <h3>{editing ? "Edit Profile" : cloning ? `Clone ${cloning.name}` : "Create New Profile"}</h3>
          <ProfileForm
            key={editing?.name ?? (cloning ? `clone-${cloning.name}` : "create")}
            profile={editing}
            seed={cloning}
            onSave={handleSave}
            onCancel={() => { setEditing(null); setCloning(null); setShowCreate(false); }}
          />
        </section>
      )}
    </div>
  );
}

function isABRProfile(profile: PackageProfile): boolean {
  return (profile.tags ?? []).includes("abr");
}

function isABRBridgeProfile(profile: PackageProfile): boolean {
  return profile.mediaKind !== "music" && (profile.tags ?? []).includes("default");
}

type LadderGroup = { label: string; profiles: PackageProfile[] };

const LADDER_DEFS: { label: string; names: string[] }[] = [
  { label: "CPU",  names: ["h264-copy-source", "h264-main-1080p", "h264-main-720p", "h264-main-480p"] },
  { label: "NVENC", names: ["h264-nvenc-copy-source", "h264-nvenc-main-1080p", "h264-nvenc-main-720p", "h264-nvenc-main-480p"] },
  { label: "HDR",  names: ["hevc-copy-source", "h264-main-1080p"] },
];

function buildLadderGroups(profiles: PackageProfile[]): LadderGroup[] {
  const byName = new Map(profiles.map((p) => [p.name, p]));
  return LADDER_DEFS.map((def) => ({
    label: def.label,
    profiles: def.names.map((n) => byName.get(n)).filter((p): p is PackageProfile => p != null),
  }));
}

function abrGroupProfiles(profiles: PackageProfile[]): PackageProfile[] {
  const grouped = profiles.filter((p) => isABRProfile(p) || isABRBridgeProfile(p));
  const order = new Map([
    ["h264-copy-source", 0],
    ["h264-main-1080p", 1],
    ["h264-main-720p", 2],
    ["h264-main-480p", 3],
    ["h264-nvenc-copy-source", 10],
    ["h264-nvenc-main-1080p", 11],
    ["h264-nvenc-main-720p", 12],
    ["h264-nvenc-main-480p", 13],
    ["hevc-copy-source", 20],
  ]);
  return grouped.sort((a, b) => (order.get(a.name) ?? 100) - (order.get(b.name) ?? 100));
}

function ABRProfileDetail({ profiles, ladderGroups }: { profiles: PackageProfile[]; ladderGroups: LadderGroup[] }) {
  return (
    <div className={styles["profile-detail"]}>
      <p className={styles["profile-detail-desc"]}>
        Three standard ABR ladders are available. Each ladder is assigned as a unit when creating a channel with adaptive bitrate enabled.
      </p>
      {ladderGroups.map((group) => group.profiles.length > 0 && (
        <fieldset key={group.label} className={styles["profile-ladder-group"]}>
          <legend>{group.label} ladder ({group.profiles.length} rungs)</legend>
          <div className={styles["profile-detail-grid"]}>
            {group.profiles.map((p) => (
              <section key={p.name}>
                <h4>{p.label}</h4>
                <dl>
                  <dt>Profile</dt><dd>{p.name}</dd>
                  <dt>Video</dt><dd>{formatOutput(p)}</dd>
                  <dt>Audio</dt><dd>{formatAudioCell(p)}</dd>
                  <dt>Status</dt><dd>{p.disabled ? "Disabled" : "Active"}</dd>
                </dl>
              </section>
            ))}
          </div>
        </fieldset>
      ))}
    </div>
  );
}

function formatABROutput(profiles: PackageProfile[]): string {
  const labels = profiles.map((p) => p.video.mode === "copy" ? "source" : p.video.scaleHeight ? `${p.video.scaleHeight}p` : "source");
  return labels.join(" / ");
}

function formatABRAudio(profiles: PackageProfile[]): string {
  const transcodes = profiles.filter((p) => p.audio.mode === "transcode");
  if (transcodes.length === profiles.length) return "AAC";
  return "mixed";
}

function ProfileDetail({ profile }: { profile: PackageProfile }) {
  const v = profile.video;
  const a = profile.audio;
  return (
    <div className={styles["profile-detail"]}>
      {profile.description && <p className={styles["profile-detail-desc"]}>{profile.description}</p>}
      <div className={styles["profile-detail-grid"]}>
        <section>
          <h4>Video</h4>
          <dl>
            <dt>Mode</dt><dd>{v.mode}</dd>
            {v.mode === "transcode" && (
              <>
                <dt>Codec</dt><dd>{v.codec || "—"}</dd>
                <dt>H.264 profile</dt><dd>{v.profile || "—"}</dd>
                <dt>Preset</dt><dd>{v.preset || "—"}</dd>
                <dt>Rate control</dt><dd>{formatRateControl(v)}</dd>
                <dt>Scale height</dt><dd>{v.scaleHeight ? `${v.scaleHeight}p` : "source"}</dd>
                <dt>Pixel format</dt><dd>yuv420p <span className="muted">(fixed)</span></dd>
              </>
            )}
            {v.mode === "copy" && (
              <>
                <dt>Required codec</dt><dd>{v.codecRequired || "—"}</dd>
              </>
            )}
          </dl>
        </section>
        <section>
          <h4>Audio</h4>
          <dl>
            <dt>Mode</dt><dd>{a.mode}</dd>
            {a.mode === "transcode" && (
              <>
                <dt>Codec</dt><dd>{a.codec || "—"}</dd>
                <dt>Bitrate</dt><dd>{a.bitrate || "—"}</dd>
                <dt>Channels</dt><dd>{a.channels ? channelsLabel(a.channels) : "—"}</dd>
                <dt>Sample rate</dt><dd>{a.sampleHz ? `${Math.round(a.sampleHz / 1000)} kHz` : "—"}</dd>
              </>
            )}
          </dl>
        </section>
        <section>
          <h4>HLS</h4>
          <dl>
            <dt>Segment type</dt><dd>fmp4 <span className="muted">(fixed)</span></dd>
            <dt>Keyframe align</dt><dd>forced <span className="muted">(HLS requires)</span></dd>
          </dl>
        </section>
      </div>
    </div>
  );
}

function formatRateControl(v: PackageProfile["video"]): string {
  if (v.crf) return `CRF ${v.crf}`;
  if (v.videoQuality) return `Q ${v.videoQuality}`;
  if (v.videoBitrate) {
    return v.videoMaxBitrate
      ? `${v.videoBitrate} (max ${v.videoMaxBitrate})`
      : v.videoBitrate;
  }
  return "—";
}

function encoderTierKey(p: PackageProfile): string {
  if (p.video.mode === "copy") return "copy";
  switch (p.video.codec) {
    case "libx264": return "cpu";
    case "h264_videotoolbox": return "apple";
    case "h264_nvenc": return "nvidia";
    default: return "other";
  }
}

function formatEncoderTier(p: PackageProfile): string {
  if (p.video.mode === "copy") return "Copy";
  switch (p.video.codec) {
    case "libx264": return "CPU";
    case "h264_videotoolbox": return "Apple";
    case "h264_nvenc": return "NVIDIA";
    default: return p.video.codec || "—";
  }
}

function formatOutput(p: PackageProfile): string {
  const v = p.video;
  if (v.mode === "copy") return v.codecRequired ? `copy ${v.codecRequired}` : "copy";
  const res = v.scaleHeight ? `${v.scaleHeight}p` : "source";
  let rate = "";
  if (v.crf) rate = `CRF ${v.crf}`;
  else if (v.videoQuality) rate = `Q ${v.videoQuality}`;
  else if (v.videoBitrate) rate = v.videoBitrate;
  return rate ? `${res} · ${rate}` : res;
}

function formatAudioCell(p: PackageProfile): string {
  const a = p.audio;
  if (a.mode === "copy") return "copy";
  const parts: string[] = [];
  if (a.codec) parts.push(a.codec.toUpperCase());
  if (a.bitrate) parts.push(a.bitrate);
  if (a.channels) parts.push(channelsLabel(a.channels));
  return parts.length ? parts.join(" ") : "—";
}

function channelsLabel(channels: number): string {
  if (channels === 1) return "mono";
  if (channels === 2) return "stereo";
  if (channels === 6) return "5.1";
  if (channels === 8) return "7.1";
  return `${channels}ch`;
}

function profileHasReferences(profile: PackageProfile) {
  const refs = profile.references;
  return !!refs && (refs.mediaPackages > 0 || refs.channels > 0 || refs.scheduleEntries > 0);
}

function packageProfileToSimpleDraft(profile: PackageProfile | null): SimpleProfileDraft {
  if (!profile) return DEFAULT_SIMPLE_DRAFT;
  const encoder = encoderFromProfile(profile);
  const resolution = resolutionFromProfile(profile);
  return {
    encoder,
    resolution,
    quality: qualityFromProfile(profile, encoder, resolution),
  };
}

function encoderFromProfile(profile: PackageProfile): EncoderChoice {
  if (profile.video.mode === "copy") return "copy";
  if (profile.video.codec === "h264_videotoolbox") return "apple";
  if (profile.video.codec === "h264_nvenc") return "nvidia";
  return "cpu";
}

function resolutionFromProfile(profile: PackageProfile): ResolutionChoice {
  return heightToResolution(profile.video.scaleHeight);
}

function heightToResolution(height?: number): ResolutionChoice {
  if (height === 1080) return "1080";
  if (height === 720) return "720";
  if (height === 480) return "480";
  return "source";
}

function qualityFromProfile(profile: PackageProfile, encoder: EncoderChoice, resolution: ResolutionChoice): QualityChoice {
  if (encoder === "nvidia" && profile.video.videoBitrate) {
    const matched = qualityForBitrate(profile.video.videoBitrate, resolution);
    if (matched) return matched;
  }
  if (profile.video.crf) {
    if (profile.video.crf <= X264_CRF.best) return "best";
    if (profile.video.crf <= X264_CRF.better) return "better";
    return "good";
  }
  if (profile.video.videoQuality) {
    if (profile.video.videoQuality >= VIDEOTOOLBOX_QUALITY.best) return "best";
    if (profile.video.videoQuality >= VIDEOTOOLBOX_QUALITY.better) return "better";
    return "good";
  }
  return "better";
}

function qualityForBitrate(bitrate: string, resolution: ResolutionChoice): QualityChoice | null {
  for (const quality of ["good", "better", "best"] as const) {
    if (bitrate === QUALITY_BITRATES[resolution][quality]) return quality;
  }
  return null;
}

function settingsForSimpleDraft(draft: SimpleProfileDraft): ProfileSettings {
  const audio: PackageProfile["audio"] = {
    mode: "transcode",
    codec: "aac",
    bitrate: "192k",
    channels: 2,
    sampleHz: 48000,
  };
  const height = resolutionToHeight(draft.resolution);
  if (draft.encoder === "copy") {
    const video: PackageProfile["video"] = {
      mode: "copy",
      codecRequired: "h264",
    };
    return { video, audio };
  }

  const video: PackageProfile["video"] = {
    mode: "transcode",
    codec: codecForEncoder(draft.encoder),
    profile: "main",
  };
  if (height) video.scaleHeight = height;

  if (draft.encoder === "cpu") {
    video.preset = draft.quality === "good" ? "veryfast" : "medium";
    video.crf = X264_CRF[draft.quality];
  } else if (draft.encoder === "apple") {
    video.videoQuality = VIDEOTOOLBOX_QUALITY[draft.quality];
  } else {
    video.preset = draft.quality === "best" ? "p5" : "p4";
    video.videoBitrate = QUALITY_BITRATES[draft.resolution][draft.quality];
    video.videoMaxBitrate = maxBitrateFor(video.videoBitrate);
  }

  return { video, audio };
}

function codecForEncoder(encoder: EncoderChoice) {
  switch (encoder) {
    case "apple":
      return "h264_videotoolbox";
    case "nvidia":
      return "h264_nvenc";
    case "cpu":
    default:
      return "libx264";
  }
}

function resolutionToHeight(resolution: ResolutionChoice) {
  return resolution === "source" ? undefined : parseInt(resolution, 10);
}

function maxBitrateFor(bitrate: string) {
  const match = /^(\d+)k$/i.exec(bitrate.trim());
  if (!match) return "";
  return `${Math.round(parseInt(match[1], 10) * 1.5)}k`;
}

function identityForSimpleDraft(draft: SimpleProfileDraft) {
  const encoderSlug: Record<EncoderChoice, string> = {
    cpu: "h264-main",
    apple: "h264-videotoolbox",
    nvidia: "h264-nvenc",
    copy: "h264-copy",
  };
  const resolutionSlug = draft.resolution === "source" ? "source" : `${draft.resolution}p`;
  const name = draft.encoder === "copy"
    ? `${encoderSlug[draft.encoder]}-${resolutionSlug}`
    : `${encoderSlug[draft.encoder]}-${draft.quality}-${resolutionSlug}`;
  const label = draft.encoder === "copy"
    ? `${resolutionLabel(draft.resolution)} H.264 copy`
    : `${resolutionLabel(draft.resolution)} ${qualityLabel(draft.quality)} ${encoderLabel(draft.encoder)}`;
  return {
    name,
    label,
    description: summaryForSimpleDraft(draft),
  };
}

function summaryForSimpleDraft(draft: SimpleProfileDraft) {
  if (draft.encoder === "copy") {
    return `Copy H.264 video at ${resolutionLabel(draft.resolution).toLowerCase()} and transcode audio to AAC stereo.`;
  }
  return `${qualityLabel(draft.quality)} ${encoderLabel(draft.encoder)} encode at ${resolutionLabel(draft.resolution).toLowerCase()} with AAC stereo.`;
}

function encoderLabel(encoder: EncoderChoice) {
  return ENCODER_OPTIONS.find((item) => item.value === encoder)?.label ?? encoder;
}

function resolutionLabel(resolution: ResolutionChoice) {
  return RESOLUTION_OPTIONS.find((item) => item.value === resolution)?.label ?? resolution;
}

function qualityLabel(quality: QualityChoice) {
  return QUALITY_OPTIONS.find((item) => item.value === quality)?.label ?? quality;
}

function parseOptionalInt(value: string) {
  const trimmed = value.trim();
  return trimmed ? parseInt(trimmed, 10) : undefined;
}

function overridesFromProfile(profile: PackageProfile, draft: SimpleProfileDraft): OverrideState {
  const state = emptyOverrideState();
  const tier = settingsForSimpleDraft(draft);
  const v = profile.video;
  const tv = tier.video;
  if (v.mode !== tv.mode) return state;
  if (v.crf && v.crf !== tv.crf) state.crf = { enabled: true, value: String(v.crf) };
  if (v.videoQuality && v.videoQuality !== tv.videoQuality) state.videoQuality = { enabled: true, value: String(v.videoQuality) };
  if (v.preset && v.preset !== tv.preset) state.preset = { enabled: true, value: v.preset };
  if (v.videoBitrate && v.videoBitrate !== tv.videoBitrate) state.videoBitrate = { enabled: true, value: v.videoBitrate };
  if (v.videoMaxBitrate && v.videoMaxBitrate !== tv.videoMaxBitrate) state.videoMaxBitrate = { enabled: true, value: v.videoMaxBitrate };
  if (v.scaleHeight && v.scaleHeight !== tv.scaleHeight) state.scaleHeight = { enabled: true, value: String(v.scaleHeight) };
  if (v.profile && v.profile !== tv.profile) state.h264Profile = { enabled: true, value: v.profile };
  return state;
}

function applyOverrides(base: ProfileSettings, encoder: EncoderChoice, overrides: OverrideState): ProfileSettings {
  const video: PackageProfile["video"] = { ...base.video };
  const applicable = new Set(applicableOverrides(encoder));
  for (const keyStr of Object.keys(overrides)) {
    const key = keyStr as OverrideKey;
    if (!applicable.has(key)) continue;
    const o = overrides[key];
    if (!o.enabled || o.value.trim() === "") continue;
    switch (key) {
      case "crf": video.crf = parseInt(o.value, 10); break;
      case "videoQuality": video.videoQuality = parseInt(o.value, 10); break;
      case "preset": video.preset = o.value.trim(); break;
      case "videoBitrate": video.videoBitrate = o.value.trim(); break;
      case "videoMaxBitrate": video.videoMaxBitrate = o.value.trim(); break;
      case "scaleHeight": video.scaleHeight = parseInt(o.value, 10); break;
      case "h264Profile": video.profile = o.value.trim(); break;
    }
  }
  return { video, audio: base.audio };
}

function ProfileForm({
  profile,
  seed,
  onSave,
  onCancel,
}: {
  profile: PackageProfile | null;
  seed?: PackageProfile | null;
  onSave: (p: PackageProfile) => void;
  onCancel: () => void;
}) {
  const initSource = profile ?? seed ?? null;
  const initialSimpleDraft = packageProfileToSimpleDraft(initSource);
  const initialIdentity = identityForSimpleDraft(initialSimpleDraft);
  const initialOverrides = initSource
    ? overridesFromProfile(initSource, initialSimpleDraft)
    : emptyOverrideState();
  const startSeeded = !!profile || !!seed;

  const [encoder, setEncoder] = useState<EncoderChoice>(initialSimpleDraft.encoder);
  const [resolution, setResolution] = useState<ResolutionChoice>(initialSimpleDraft.resolution);
  const [quality, setQuality] = useState<QualityChoice>(initialSimpleDraft.quality);
  const [nameEdited, setNameEdited] = useState(startSeeded);
  const [labelEdited, setLabelEdited] = useState(startSeeded);
  const [descriptionEdited, setDescriptionEdited] = useState(startSeeded);
  const [name, setName] = useState(profile?.name ?? (seed ? `${seed.name}-copy` : initialIdentity.name));
  const [label, setLabel] = useState(profile?.label ?? seed?.label ?? initialIdentity.label);
  const [description, setDescription] = useState(profile?.description ?? seed?.description ?? initialIdentity.description);
  const [audioMode, setAudioMode] = useState(initSource?.audio.mode ?? "transcode");
  const [audioCodec, setAudioCodec] = useState(initSource?.audio.codec ?? "aac");
  const [audioBitrate, setAudioBitrate] = useState(initSource?.audio.bitrate ?? "192k");
  const [audioChannels, setAudioChannels] = useState(initSource?.audio.channels?.toString() ?? "2");
  const [audioSampleHz, setAudioSampleHz] = useState(initSource?.audio.sampleHz?.toString() ?? "48000");
  const [overrides, setOverrides] = useState<OverrideState>(initialOverrides);
  const [advancedOpen, setAdvancedOpen] = useState(
    Object.values(initialOverrides).some((o) => o.enabled)
  );

  const draft: SimpleProfileDraft = { encoder, resolution, quality };
  const tierSettings = settingsForSimpleDraft(draft);
  const resolved = applyOverrides(tierSettings, encoder, overrides);
  const applicableKeys = applicableOverrides(encoder);

  function updateDraft(next: Partial<SimpleProfileDraft>) {
    const updated: SimpleProfileDraft = { encoder, resolution, quality, ...next };
    setEncoder(updated.encoder);
    setResolution(updated.resolution);
    setQuality(updated.quality);
    if (!profile) {
      const nextIdentity = identityForSimpleDraft(updated);
      if (!nameEdited) setName(nextIdentity.name);
      if (!labelEdited) setLabel(nextIdentity.label);
      if (!descriptionEdited) setDescription(nextIdentity.description);
    }
  }

  function setOverrideEnabled(key: OverrideKey, enabled: boolean) {
    setOverrides((prev) => ({
      ...prev,
      [key]: {
        enabled,
        value: enabled && !prev[key].value ? defaultValueForOverride(key, resolved) : prev[key].value,
      },
    }));
  }

  function setOverrideValue(key: OverrideKey, value: string) {
    setOverrides((prev) => ({ ...prev, [key]: { ...prev[key], value } }));
  }

  function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    const audio: PackageProfile["audio"] = {
      mode: audioMode as "transcode" | "copy",
      codec: audioMode === "transcode" ? (audioCodec.trim() || undefined) : undefined,
      bitrate: audioMode === "transcode" ? (audioBitrate.trim() || undefined) : undefined,
      channels: audioMode === "transcode" ? parseOptionalInt(audioChannels) : undefined,
      sampleHz: audioMode === "transcode" ? parseOptionalInt(audioSampleHz) : undefined,
    };
    const p: PackageProfile = {
      name: name.trim(),
      label: label.trim(),
      description: description.trim(),
      video: resolved.video,
      audio,
    };
    onSave(p);
  }

  return (
    <form className={styles["profile-form"]} onSubmit={handleSubmit}>
      <div className={styles["profile-form-meta"]}>
        <label>
          <span>Name</span>
          <input
            value={name}
            onChange={(e) => { setNameEdited(true); setName(e.target.value); }}
            required
            disabled={!!profile}
          />
        </label>
        <label>
          <span>Label</span>
          <input
            value={label}
            onChange={(e) => { setLabelEdited(true); setLabel(e.target.value); }}
            required
          />
        </label>
        <label>
          <span>Description</span>
          <input
            value={description}
            onChange={(e) => { setDescriptionEdited(true); setDescription(e.target.value); }}
          />
        </label>
      </div>

      <fieldset className={styles["profile-simple-settings"]}>
        <legend>Profile setup</legend>
        <div className={styles["profile-simple-grid"]}>
          <label>
            <span>Encoder</span>
            <select value={encoder} onChange={(e) => updateDraft({ encoder: e.target.value as EncoderChoice })}>
              {ENCODER_OPTIONS.map((item) => <option key={item.value} value={item.value}>{item.label}</option>)}
            </select>
          </label>
          <label>
            <span>Resolution</span>
            <select value={resolution} onChange={(e) => updateDraft({ resolution: e.target.value as ResolutionChoice })}>
              {RESOLUTION_OPTIONS.map((item) => <option key={item.value} value={item.value}>{item.label}</option>)}
            </select>
          </label>
          <label>
            <span>Quality</span>
            <select
              value={quality}
              disabled={encoder === "copy"}
              onChange={(e) => updateDraft({ quality: e.target.value as QualityChoice })}
            >
              {QUALITY_OPTIONS.map((item) => <option key={item.value} value={item.value}>{item.label}</option>)}
            </select>
          </label>
        </div>
      </fieldset>

      <fieldset className={styles["profile-resolved"]}>
        <legend>Resolved settings</legend>
        <dl>
          <dt>Mode</dt><dd>{resolved.video.mode}</dd>
          {resolved.video.mode === "transcode" && (
            <>
              <dt>Codec</dt><dd>{resolved.video.codec || "—"}</dd>
              <dt>H.264 profile</dt><dd>{resolved.video.profile || "—"}</dd>
              <dt>Preset</dt><dd>{resolved.video.preset || "—"}</dd>
              <dt>Rate control</dt><dd>{formatRateControl(resolved.video)}</dd>
              <dt>Scale height</dt><dd>{resolved.video.scaleHeight ? `${resolved.video.scaleHeight}p` : "source"}</dd>
              <dt>Pixel format</dt><dd>yuv420p <span className="muted">(fixed)</span></dd>
              <dt>HLS segment</dt><dd>fmp4 <span className="muted">(fixed)</span></dd>
            </>
          )}
          {resolved.video.mode === "copy" && (
            <>
              <dt>Required codec</dt><dd>{resolved.video.codecRequired || "—"}</dd>
            </>
          )}
        </dl>
      </fieldset>

      {applicableKeys.length > 0 && (
        <fieldset className={styles["profile-overrides"]}>
          <legend>
            <button
              type="button"
              className={styles["profile-advanced-toggle"]}
              aria-expanded={advancedOpen}
              onClick={() => setAdvancedOpen((open) => !open)}
            >
              {advancedOpen ? "Hide advanced overrides" : "Show advanced overrides"}
            </button>
          </legend>
          {advancedOpen && (
            <div className={styles["profile-overrides-grid"]}>
              {applicableKeys.map((key) => {
                const meta = OVERRIDE_META[key];
                const o = overrides[key];
                return (
                  <div key={key} className={`${styles["profile-override-row"]}${o.enabled ? " is-on" : ""}`}>
                    <label className={styles["profile-override-toggle"]}>
                      <input
                        type="checkbox"
                        checked={o.enabled}
                        onChange={(e) => setOverrideEnabled(key, e.target.checked)}
                      />
                      <span>{meta.label}</span>
                    </label>
                    <input
                      type={meta.type ?? "text"}
                      placeholder={meta.placeholder}
                      value={o.value}
                      disabled={!o.enabled}
                      onChange={(e) => setOverrideValue(key, e.target.value)}
                    />
                  </div>
                );
              })}
            </div>
          )}
        </fieldset>
      )}

      <fieldset>
        <legend>Audio</legend>
        <label>
          <span>Audio Mode</span>
          <select value={audioMode} onChange={(e) => setAudioMode(e.target.value)}>
            <option value="transcode">Transcode</option>
            <option value="copy">Copy</option>
          </select>
        </label>
        {audioMode === "transcode" && (
          <>
            <label><span>Codec</span><input value={audioCodec} onChange={(e) => setAudioCodec(e.target.value)} /></label>
            <label><span>Bitrate</span><input value={audioBitrate} onChange={(e) => setAudioBitrate(e.target.value)} placeholder="192k" /></label>
            <label><span>Channels</span><input value={audioChannels} onChange={(e) => setAudioChannels(e.target.value)} type="number" /></label>
            <label><span>Sample Hz</span><input value={audioSampleHz} onChange={(e) => setAudioSampleHz(e.target.value)} type="number" /></label>
          </>
        )}
      </fieldset>

      <div className={styles["form-actions"]}>
        <button type="submit" className="primary">{profile ? "Update" : "Create"}</button>
        <button type="button" onClick={onCancel}>Cancel</button>
      </div>
    </form>
  );
}

function defaultValueForOverride(key: OverrideKey, resolved: ProfileSettings): string {
  const v = resolved.video;
  switch (key) {
    case "crf": return v.crf ? String(v.crf) : "";
    case "videoQuality": return v.videoQuality ? String(v.videoQuality) : "";
    case "preset": return v.preset ?? "";
    case "videoBitrate": return v.videoBitrate ?? "";
    case "videoMaxBitrate": return v.videoMaxBitrate ?? "";
    case "scaleHeight": return v.scaleHeight ? String(v.scaleHeight) : "";
    case "h264Profile": return v.profile ?? "";
  }
}
