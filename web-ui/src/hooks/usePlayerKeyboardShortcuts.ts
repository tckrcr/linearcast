import { useEffect } from "react";
import type { PlayableSource } from "../types";

type UsePlayerKeyboardShortcutsOptions = {
  sortedChannels: PlayableSource[];
  activeChannelID: string | null;
  setActiveChannelID: (id: string | null) => void;
  muted: boolean;
  setMuted: (muted: boolean) => void;
  abrMode: "best" | "saver";
  setAbrMode: (mode: "best" | "saver") => void;
  debugOpen: boolean;
  setDebugOpen: (open: boolean) => void;
  channelsOpen: boolean;
  setChannelsOpen: (open: boolean) => void;
};

export function usePlayerKeyboardShortcuts({
  sortedChannels,
  activeChannelID,
  setActiveChannelID,
  muted,
  setMuted,
  abrMode,
  setAbrMode,
  debugOpen,
  setDebugOpen,
  channelsOpen,
  setChannelsOpen,
}: UsePlayerKeyboardShortcutsOptions) {
  useEffect(() => {
    function cycleChannel(direction: 1 | -1) {
      if (sortedChannels.length === 0) return;
      const idx = sortedChannels.findIndex((c) => c.id === activeChannelID);
      const nextIdx =
        idx === -1
          ? 0
          : (idx + direction + sortedChannels.length) % sortedChannels.length;
      setActiveChannelID(sortedChannels[nextIdx].id);
    }

    function toggleFullscreen() {
      if (document.fullscreenElement) {
        document.exitFullscreen().catch(() => {});
      } else {
        document.documentElement.requestFullscreen().catch(() => {});
      }
    }

    function onKey(e: KeyboardEvent) {
      const target = e.target as HTMLElement | null;
      if (
        target &&
        (target.tagName === "INPUT" ||
          target.tagName === "TEXTAREA" ||
          target.tagName === "SELECT" ||
          target.isContentEditable)
      ) {
        return;
      }
      if (e.metaKey || e.ctrlKey || e.altKey) return;

      switch (e.key) {
        case "ArrowUp":
          e.preventDefault();
          cycleChannel(-1);
          break;
        case "ArrowDown":
          e.preventDefault();
          cycleChannel(1);
          break;
        case "m":
        case "M":
          setMuted(!muted);
          break;
        case "q":
        case "Q":
          setAbrMode(abrMode === "best" ? "saver" : "best");
          break;
        case "f":
        case "F":
          toggleFullscreen();
          break;
        case "Escape":
          if (debugOpen) setDebugOpen(false);
          else if (channelsOpen) setChannelsOpen(false);
          break;
      }
    }

    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [
    activeChannelID,
    abrMode,
    channelsOpen,
    debugOpen,
    muted,
    setAbrMode,
    setActiveChannelID,
    setChannelsOpen,
    setDebugOpen,
    setMuted,
    sortedChannels,
  ]);
}
