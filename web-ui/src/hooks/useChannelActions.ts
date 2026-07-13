import { useState } from "react";
import {
  clearChannelSchedule as apiClearSchedule,
  cloneChannel as apiCloneChannel,
  deleteChannel as apiDeleteChannel,
  extendChannel as apiExtendChannel,
  patchChannel as apiPatchChannel,
  resetChannelArtwork as apiResetChannelArtwork,
  restartChannelPlayback as apiRestartPlayback,
  updateChannelArtwork as apiUpdateChannelArtwork,
  updateChannelOnDemandProfile as apiUpdateChannelOnDemandProfile,
  updateChannelUpstreamHLS as apiUpdateChannelUpstreamHLS,
} from "../api";
import { formatBytes } from "../format";
import type { PackageProfile, RowBusy, RowStatus } from "../types";

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

  function setBusy(id: string, v: boolean) {
    setRowBusy((prev) => ({ ...prev, [id]: v }));
  }

  function setStatus(id: string, msg: string) {
    setRowStatus((prev) => ({ ...prev, [id]: msg }));
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
      await apiPatchChannel(channelID, { enabled });
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
      await apiPatchChannel(channelID, { hiddenFromGuide: hidden });
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

  async function deleteChannel(
    channelID: string,
    displayName: string,
    reclaimEncodes: boolean,
    wasEnabled: boolean,
  ) {
    setBusy(channelID, true);
    setStatus(channelID, reclaimEncodes ? "deleting + reclaiming…" : "deleting…");
    try {
      // The server refuses to delete an enabled channel so a live row is never
      // stranded. Disable it first so the user still gets one-step deletion.
      if (wasEnabled) await apiPatchChannel(channelID, { enabled: false });
      const res = await apiDeleteChannel(channelID, { reclaimEncodes });
      setRowStatus((prev) => {
        const next = { ...prev };
        delete next[channelID];
        return next;
      });
      if (reclaimEncodes && res.reclaim) {
        const r = res.reclaim;
        const skipped = r.skippedRows
          ? `; kept ${r.skippedRows} encode package(s) still used by another channel`
          : "";
        window.alert(
          `Deleted "${displayName || channelID}".\n\nReclaimed ${r.deletedRows} encode package(s) (${formatBytes(r.totalBytes)})${skipped}. Source media files were not deleted.`,
        );
      }
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

  async function changeOnDemandProfile(
    channelID: string,
    displayName: string,
    currentProfile: string,
    mediaKind: string,
  ) {
    const kindProfiles = profilesForMediaKind(allowedProfiles, profileDetails, mediaKind);
    if (kindProfiles.length === 0) {
      setStatus(channelID, "no package profiles are available for this channel");
      return;
    }
    const next = window.prompt(
      `Package profile for "${displayName || channelID}"\n\nAvailable profiles:\n${kindProfiles.join("\n")}`,
      currentProfile,
    );
    if (next == null) return;
    const profile = next.trim();
    if (profile === currentProfile) return;
    if (!profile) {
      setStatus(channelID, "profile cannot be empty");
      return;
    }
    if (!kindProfiles.includes(profile)) {
      setStatus(channelID, `unknown ${normalizeChannelMediaKind(mediaKind)} profile "${profile}"`);
      return;
    }
    setBusy(channelID, true);
    setStatus(channelID, "saving profile...");
    try {
      const policy = await apiUpdateChannelOnDemandProfile(channelID, profile);
      setStatus(channelID, `profile saved: ${policy.requiredPackageProfile}`);
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

  return {
    rowBusy,
    rowStatus,
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
    changeOnDemandProfile,
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
  return allowedProfiles.filter((profile) => {
    const detail = profileDetails[profile];
    return normalizeChannelMediaKind(detail?.mediaKind) === kind;
  });
}
