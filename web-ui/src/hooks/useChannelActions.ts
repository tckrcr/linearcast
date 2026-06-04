import { useState } from "react";
import {
  clearChannelSchedule as apiClearSchedule,
  cloneChannel as apiCloneChannel,
  deleteChannel as apiDeleteChannel,
  extendChannel as apiExtendChannel,
  resetChannelArtwork as apiResetChannelArtwork,
  getChannelPolicy,
  getChannelProfileMigrationStatus,
  queueChannelProfileMigration,
  restartChannelPlayback as apiRestartPlayback,
  setChannelEnabled as apiSetChannelEnabled,
  setChannelHiddenFromGuide as apiSetChannelHiddenFromGuide,
  updateChannelArtwork as apiUpdateChannelArtwork,
  updateChannelPolicy,
  updateChannelUpstreamHLS as apiUpdateChannelUpstreamHLS,
} from "../api";
import { ApiError } from "../api/client";
import type { PackageProfile, PolicyDraft, ProfileReadiness, RowBusy, RowStatus } from "../types";

const DEFAULT_PREFILL_HOURS = "24";

type UseChannelActionsOptions = {
  allowedProfiles: string[];
  profileDetails: Record<string, PackageProfile>;
  refreshChannels: () => void;
  selected: string;
  setSelected: (selected: string) => void;
};

export function useChannelActions({
  allowedProfiles,
  profileDetails,
  refreshChannels,
  selected,
  setSelected,
}: UseChannelActionsOptions) {
  const [rowBusy, setRowBusy] = useState<RowBusy>({});
  const [rowStatus, setRowStatus] = useState<RowStatus>({});
  const [policyDraft, setPolicyDraft] = useState<Record<string, PolicyDraft>>({});
  const [migrationReadiness, setMigrationReadiness] = useState<Record<string, ProfileReadiness | null>>({});

  function setBusy(id: string, v: boolean) {
    setRowBusy((prev) => ({ ...prev, [id]: v }));
  }

  function setStatus(id: string, msg: string) {
    setRowStatus((prev) => ({ ...prev, [id]: msg }));
  }

  async function loadPolicy(channelID: string) {
    setBusy(channelID, true);
    try {
      const policy = await getChannelPolicy(channelID);
      setPolicyDraft((prev) => ({
        ...prev,
        [channelID]: {
          profile: policy.requiredPackageProfile,
          prefillHours: msToHoursInput(policy.packagePrefillMs),
          mediaKind: normalizeChannelMediaKind(policy.mediaKind),
          loaded: true,
        },
      }));
    } catch (err) {
      setStatus(channelID, err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(channelID, false);
    }
  }

  async function extendSchedule(channelID: string, hours?: number) {
    setBusy(channelID, true);
    setStatus(channelID, "extending schedule…");
    try {
      const res = await apiExtendChannel(channelID, hours);
      setStatus(
        channelID,
        res.note
          ? res.note
          : res.skippedLowWater
            ? "skipped — coverage above low-water"
            : `inserted ${res.inserted} entries`,
      );
    } catch (err) {
      setStatus(channelID, err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(channelID, false);
    }
  }

  async function clearSchedule(channelID: string, displayName: string) {
    if (
      !window.confirm(
        `Clear all scheduled entries for "${displayName || channelID}"?\n\nThe channel will show as gap/unscheduled until re-extended.`,
      )
    )
      return;
    setBusy(channelID, true);
    setStatus(channelID, "clearing schedule…");
    try {
      const res = await apiClearSchedule(channelID);
      setStatus(channelID, `cleared ${res.cleared} entries`);
    } catch (err) {
      setStatus(channelID, err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(channelID, false);
    }
  }

  async function restartPlayback(channelID: string, displayName: string) {
    if (
      !window.confirm(
        `Restart playback for "${displayName || channelID}"?\n\nClears the schedule and re-extends from now. Viewers will see a brief gap until linearcast refreshes (~60s).`,
      )
    )
      return;
    setBusy(channelID, true);
    setStatus(channelID, "restarting playback…");
    try {
      const res = await apiRestartPlayback(channelID);
      setStatus(
        channelID,
        res.warning
          ? `cleared ${res.cleared}; ${res.warning}`
          : `cleared ${res.cleared}, inserted ${res.inserted}`,
      );
    } catch (err) {
      setStatus(channelID, err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(channelID, false);
    }
  }

  async function setEnabled(channelID: string, displayName: string, enabled: boolean) {
    if (
      !enabled &&
      !window.confirm(
        `Disable channel "${displayName || channelID}"?\n\nIt will stop being scheduled and disappear from the grid within ~60s.`,
      )
    )
      return;
    setBusy(channelID, true);
    setStatus(channelID, enabled ? "enabling…" : "disabling…");
    try {
      await apiSetChannelEnabled(channelID, enabled);
      setStatus(
        channelID,
        enabled ? "enabled — picks up on next refresh tick" : "disabled — drops on next refresh tick",
      );
      refreshChannels();
      if (!enabled && selected === channelID) setSelected("tools");
    } catch (err) {
      setStatus(channelID, err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(channelID, false);
    }
  }

  async function setHiddenFromGuide(channelID: string, displayName: string, hidden: boolean) {
    setBusy(channelID, true);
    setStatus(channelID, hidden ? "hiding from guide..." : "showing in guide...");
    try {
      await apiSetChannelHiddenFromGuide(channelID, hidden);
      setStatus(
        channelID,
        hidden
          ? "hidden from public guide listings"
          : "visible in public guide listings",
      );
      refreshChannels();
    } catch (err) {
      setStatus(channelID, err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(channelID, false);
    }
  }

  async function deleteChannel(channelID: string, displayName: string) {
    if (
      !window.confirm(
        `Delete disabled channel "${displayName || channelID}"?\n\nThis removes the channel row, playlist membership, and schedule entries. Packaged media is kept.`,
      )
    )
      return;
    setBusy(channelID, true);
    setStatus(channelID, "deleting…");
    try {
      await apiDeleteChannel(channelID);
      setRowStatus((prev) => {
        const next = { ...prev };
        delete next[channelID];
        return next;
      });
      refreshChannels();
      if (selected === channelID) setSelected("tools");
    } catch (err) {
      setStatus(channelID, err instanceof Error ? err.message : String(err));
      setBusy(channelID, false);
    }
  }

  async function cloneChannel(channelID: string, displayName: string) {
    setBusy(channelID, true);
    setStatus(channelID, `duplicating ${displayName || channelID}…`);
    try {
      const res = await apiCloneChannel(channelID);
      setStatus(channelID, `duplicated as ${res.displayName || res.channelID}`);
      refreshChannels();
      setSelected(res.channelID);
    } catch (err) {
      setStatus(channelID, err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(channelID, false);
    }
  }

  async function updateArtwork(channelID: string, displayName: string, currentArtworkUrl?: string) {
    const next = window.prompt(
      `Artwork URL for "${displayName || channelID}"`,
      currentArtworkUrl || "",
    );
    if (next == null) return;
    setBusy(channelID, true);
    setStatus(channelID, "saving artwork...");
    try {
      const res = await apiUpdateChannelArtwork(channelID, next.trim());
      setStatus(channelID, res.artworkUrl ? "artwork saved" : "artwork reset to default");
      refreshChannels();
    } catch (err) {
      setStatus(channelID, err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(channelID, false);
    }
  }

  async function updateUpstreamHLS(channelID: string, url: string) {
    setBusy(channelID, true);
    setStatus(channelID, "saving...");
    try {
      await apiUpdateChannelUpstreamHLS(channelID, url);
      setStatus(channelID, "saved");
      refreshChannels();
    } catch (err) {
      setStatus(channelID, err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(channelID, false);
    }
  }

  async function resetArtwork(channelID: string, displayName: string) {
    if (!window.confirm(`Reset artwork for "${displayName || channelID}" to the built-in default?`)) return;
    setBusy(channelID, true);
    setStatus(channelID, "resetting artwork...");
    try {
      await apiResetChannelArtwork(channelID);
      setStatus(channelID, "artwork reset to default");
      refreshChannels();
    } catch (err) {
      setStatus(channelID, err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(channelID, false);
    }
  }

  async function doSavePolicy(channelID: string, force: boolean) {
    const draft = policyDraft[channelID];
    if (!draft) return;
    const validation = validateChannelPolicy(draft, allowedProfiles, profileDetails);
    if (!validation.valid) {
      setStatus(channelID, validation.message);
      return;
    }
    const policy = await updateChannelPolicy(channelID, {
      requiredPackageProfile: validation.profile,
      packagePrefillMs: validation.packagePrefillMs,
      mediaKind: validation.mediaKind,
      force,
    });
    setPolicyDraft((prev) => ({
      ...prev,
      [channelID]: {
        profile: policy.requiredPackageProfile,
        prefillHours: msToHoursInput(policy.packagePrefillMs),
        mediaKind: normalizeChannelMediaKind(policy.mediaKind),
        loaded: true,
      },
    }));
    setMigrationReadiness((prev) => ({ ...prev, [channelID]: null }));
    setStatus(channelID, "policy saved");
    refreshChannels();
  }

  async function savePolicy(channelID: string) {
    setBusy(channelID, true);
    setStatus(channelID, "saving policy…");
    try {
      await doSavePolicy(channelID, false);
    } catch (err) {
      if (err instanceof ApiError && err.code === "profile_not_ready") {
        if (window.confirm(`${err.message}\n\nForce switch anyway?`)) {
          try {
            await doSavePolicy(channelID, true);
          } catch (err2) {
            setStatus(channelID, err2 instanceof Error ? err2.message : String(err2));
          }
        } else {
          setStatus(channelID, "save cancelled");
        }
      } else {
        setStatus(channelID, err instanceof Error ? err.message : String(err));
      }
    } finally {
      setBusy(channelID, false);
    }
  }

  async function queueMigration(channelID: string, profile: string) {
    setBusy(channelID, true);
    setStatus(channelID, "queueing packaging…");
    try {
      const r = await queueChannelProfileMigration(channelID, profile);
      setMigrationReadiness((prev) => ({ ...prev, [channelID]: r }));
      setStatus(channelID, r.queued > 0 ? `queued ${r.queued} items` : "all items already queued or ready");
    } catch (err) {
      setStatus(channelID, err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(channelID, false);
    }
  }

  async function refreshMigrationStatus(channelID: string, profile: string) {
    try {
      const r = await getChannelProfileMigrationStatus(channelID, profile);
      setMigrationReadiness((prev) => ({ ...prev, [channelID]: r }));
    } catch { /* ignore — status panel is best-effort */ }
  }

  return {
    rowBusy,
    rowStatus,
    policyDraft,
    setPolicyDraft,
    migrationReadiness,
    loadPolicy,
    extendSchedule,
    clearSchedule,
    restartPlayback,
    setEnabled,
    setHiddenFromGuide,
    deleteChannel,
    cloneChannel,
    updateArtwork,
    resetArtwork,
    updateUpstreamHLS,
    savePolicy,
    queueMigration,
    refreshMigrationStatus,
  };
}

export const blankPolicyDraft: PolicyDraft = {
  profile: "",
  prefillHours: DEFAULT_PREFILL_HOURS,
  mediaKind: "video",
  loaded: false,
};

export function validateChannelPolicy(
  draft: PolicyDraft,
  allowedProfiles: string[],
  profileDetails: Record<string, PackageProfile>,
):
  | { valid: true; profile: string; packagePrefillMs: number; mediaKind: "video" | "music" }
  | { valid: false; message: string } {
  const profile = draft.profile.trim();
  const prefillHours = Number(draft.prefillHours);
  const mediaKind = normalizeChannelMediaKind(draft.mediaKind);
  if (!profile) {
    return { valid: false, message: "profile cannot be empty" };
  }
  if (!allowedProfiles.includes(profile)) {
    return {
      valid: false,
      message: `unknown profile "${profile}" — must be one of: ${allowedProfiles.join(", ")}`,
    };
  }
  const profileKind = normalizeChannelMediaKind(profileDetails[profile]?.mediaKind);
  if (profileKind !== mediaKind) {
    return {
      valid: false,
      message: `profile "${profile}" is for ${profileKind} channels`,
    };
  }
  if (!Number.isFinite(prefillHours) || prefillHours <= 0) {
    return { valid: false, message: "prefill hours must be positive" };
  }
  return {
    valid: true,
    profile,
    mediaKind,
    packagePrefillMs: Math.round(prefillHours * 3600 * 1000),
  };
}

export function normalizeChannelMediaKind(kind: string | undefined): "video" | "music" {
  return kind === "music" ? "music" : "video";
}

export function profilesForMediaKind(
  allowedProfiles: string[],
  profileDetails: Record<string, PackageProfile>,
  mediaKind: string | undefined,
): string[] {
  const kind = normalizeChannelMediaKind(mediaKind);
  return allowedProfiles.filter((profile) => normalizeChannelMediaKind(profileDetails[profile]?.mediaKind) === kind);
}

function msToHoursInput(value: number | null | undefined): string {
  if (value == null || !Number.isFinite(value)) return DEFAULT_PREFILL_HOURS;
  const hours = value / 3600000;
  return Number.isInteger(hours) ? String(hours) : hours.toFixed(2);
}
