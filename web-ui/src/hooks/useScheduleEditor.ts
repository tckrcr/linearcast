import { useCallback, useEffect, useRef, useState } from "react";
import {
  addChannelMedia as apiAddChannelMedia,
  createScheduleBuilderChannel as apiCreateScheduleBuilderChannel,
  deleteScheduleEntry as apiDeleteScheduleEntry,
  getChannelMedia,
  getChannelSchedule,
  getChannelSchedulePreview,
  getMediaPackageCandidates,
  getScheduleBuilderCandidates,
  insertScheduleEntryAfter as apiInsertScheduleEntryAfter,
  insertScheduleEntryBefore as apiInsertScheduleEntryBefore,
  saveScheduleWindowOrdered as apiSaveScheduleWindowOrdered,
  upsertScheduleEntry as apiUpsertScheduleEntry,
} from "../api";
import { SCHEDULE_GRID_MS } from "../constants";
import { formatDateTime } from "../format";
import { usePolling } from "./usePolling";
import type {
  ChannelNow,
  ChannelSchedule,
  ChannelSchedulePreview,
  DraftChannelConfig,
  ScheduleDraftEntry,
  ScheduleEditTarget,
  ScheduleInsertItem,
} from "../types";

export const WINDOW_HOURS = 24;
const SCHEDULE_SEGMENT_MS = SCHEDULE_GRID_MS;

type ScheduleDraftWindow = { fromMs: number; toMs: number };
type ScheduleMediaPickerData = {
  profile: string;
  count: number;
  media: ScheduleInsertItem[];
};
type ScheduleDraftItem = ScheduleDraftEntry & {
  requiresChannelAttach?: boolean;
};

function makeDraftInsertId(mediaId: string) {
  return `insert-${mediaId}-${Date.now()}-${Math.random().toString(36).slice(2)}`;
}

export function scheduleDurationMs(value: number | null | undefined) {
  if (value == null || !Number.isFinite(value)) return 0;
  return Math.max(0, value - (value % SCHEDULE_SEGMENT_MS));
}

export function isCurrentScheduleEntry(entry: { startMs: number; endMs: number }, nowMs: number) {
  return entry.startMs <= nowMs && nowMs < entry.endMs;
}

export function useScheduleEditor(channel: ChannelNow | null, draftConfig?: DraftChannelConfig) {
  const isDraftMode = channel === null;

  const [scheduleWindowStart, setScheduleWindowStart] = useState<number>(() => {
    const now = Date.now();
    return now - (now % (3600 * 1000));
  });
  const [scheduleData, setScheduleData] = useState<ChannelSchedule | null>(null);
  const [scheduleLoading, setScheduleLoading] = useState(false);
  const [scheduleError, setScheduleError] = useState("");
  const [scheduleNotice, setScheduleNotice] = useState("");
  const [deletingEntries, setDeletingEntries] = useState<Set<string>>(new Set());
  const [deleteBusy, setDeleteBusy] = useState(false);
  const [selectedRangeStarts, setSelectedRangeStarts] = useState<Set<string>>(new Set());
  const [lastSelectedEntryId, setLastSelectedEntryId] = useState<string | null>(null);
  const [rangeBusy, setRangeBusy] = useState(false);
  const [mediaData, setMediaData] = useState<ScheduleMediaPickerData | null>(null);
  const [channelMediaIds, setChannelMediaIds] = useState<Set<string>>(new Set());
  const [mediaLoading, setMediaLoading] = useState(false);
  const [mediaError, setMediaError] = useState("");
  const [editTarget, setEditTarget] = useState<ScheduleEditTarget | null>(null);
  const [mediaFilter, setMediaFilter] = useState("");
  const [insertBusy, setInsertBusy] = useState(false);
  const [scheduleEditMode, setScheduleEditMode] = useState(isDraftMode);
  const [scheduleDraft, setScheduleDraft] = useState<ScheduleDraftItem[]>([]);
  const [scheduleDraftUndo, setScheduleDraftUndo] = useState<ScheduleDraftItem[] | null>(null);
  const [scheduleDraftWindow, setScheduleDraftWindow] = useState<ScheduleDraftWindow | null>(null);
  const [draftInsertAt, setDraftInsertAt] = useState<number | null>(null);
  const [dragIndex, setDragIndex] = useState<number | null>(null);
  const [saveBusy, setSaveBusy] = useState(false);
  const [schedulePreview, setSchedulePreview] = useState<ChannelSchedulePreview | null>(null);
  const [previewBusy, setPreviewBusy] = useState(false);
  const [applyPreviewBusy, setApplyPreviewBusy] = useState(false);
  const deleteQueueRef = useRef<Set<string>>(new Set());
  const deleteTimerRef = useRef<number | null>(null);
  const deleteProcessingRef = useRef(false);

  const fetchSchedule = useCallback(
    async (silent: boolean, signal?: AbortSignal) => {
      if (isDraftMode || !channel) return;
      if (!silent) setScheduleLoading(true);
      setScheduleError("");
      if (!silent) setScheduleNotice("");
      try {
        const data = await getChannelSchedule(channel.id, scheduleWindowStart, WINDOW_HOURS, signal);
        setScheduleData(data);
      } catch (err) {
        if (signal?.aborted) return;
        setScheduleError(err instanceof Error ? err.message : String(err));
        throw err;
      } finally {
        if (!signal?.aborted) setScheduleLoading(false);
      }
    },
    [isDraftMode, channel?.id, scheduleWindowStart],
  );

  useEffect(() => {
    setSchedulePreview(null);
  }, [channel?.id, scheduleWindowStart]);

  // Load on mount / window change (show spinner).
  useEffect(() => { void fetchSchedule(false); }, [fetchSchedule]);

  // Auto-refresh every 60s (silent — no spinner).
  usePolling({
    enabled: !isDraftMode && !scheduleEditMode,
    intervalMs: 60_000,
    maxIntervalMs: 5 * 60_000,
    immediate: false,
    task: (signal) => fetchSchedule(true, signal),
  });

  // Reset schedule window when channel changes (non-draft only).
  useEffect(() => {
    if (!channel) return;
    const now = Date.now();
    setScheduleWindowStart(now - (now % (3600 * 1000)));
    setScheduleData(null);
    setScheduleNotice("");
    setDeletingEntries(new Set());
    setDeleteBusy(false);
    setSelectedRangeStarts(new Set());
    setLastSelectedEntryId(null);
    setRangeBusy(false);
    setMediaData(null);
    setChannelMediaIds(new Set());
    setMediaLoading(false);
    setMediaError("");
    setEditTarget(null);
    setMediaFilter("");
    setInsertBusy(false);
    setScheduleEditMode(false);
    setScheduleDraft([]);
    setScheduleDraftUndo(null);
    setScheduleDraftWindow(null);
    setDraftInsertAt(null);
    setDragIndex(null);
    setSaveBusy(false);
    setSchedulePreview(null);
    setPreviewBusy(false);
    setApplyPreviewBusy(false);
    deleteQueueRef.current.clear();
    deleteProcessingRef.current = false;
    if (deleteTimerRef.current !== null) {
      window.clearTimeout(deleteTimerRef.current);
      deleteTimerRef.current = null;
    }
  }, [channel?.id]);

  // Clear media cache when draft profile changes.
  useEffect(() => {
    if (isDraftMode) setMediaData(null);
  }, [isDraftMode, draftConfig?.packageProfile]);

  function shiftWindow(deltaHours: number) {
    if (scheduleEditMode) return;
    setScheduleWindowStart((ms) => ms + deltaHours * 3600 * 1000);
  }
  function jumpToNow() {
    if (scheduleEditMode) return;
    const now = Date.now();
    setScheduleWindowStart(now - (now % (3600 * 1000)));
  }

  async function flushDeleteQueue() {
    if (!channel || deleteProcessingRef.current) return;
    deleteProcessingRef.current = true;
    setDeleteBusy(true);
    let deleteError = "";
    try {
      while (deleteQueueRef.current.size > 0) {
        const entryIds = Array.from(deleteQueueRef.current);
        deleteQueueRef.current.clear();

        for (const entryId of entryIds) {
          try {
            await apiDeleteScheduleEntry(channel.id, entryId);
          } catch (err) {
            deleteError = err instanceof Error ? err.message : String(err);
            setScheduleError(deleteError);
            setScheduleNotice("");
          } finally {
            setDeletingEntries((prev) => {
              const next = new Set(prev);
              next.delete(entryId);
              return next;
            });
          }
        }
      }
    } finally {
      deleteProcessingRef.current = false;
      setScheduleLoading(true);
      try {
        const data = await getChannelSchedule(channel.id, scheduleWindowStart, WINDOW_HOURS);
        setScheduleData(data);
        setScheduleError(deleteError);
        if (deleteError) setScheduleNotice("");
      } catch (err) {
        setScheduleError(err instanceof Error ? err.message : String(err));
        setScheduleNotice("");
      } finally {
        setScheduleLoading(false);
        setDeleteBusy(false);
      }
    }
  }

  function deleteEntry(entryId: string) {
    const entry = scheduleData?.entries.find((item) => item.entryId === entryId);
    if (entry && isCurrentScheduleEntry(entry, Date.now())) {
      const label = entry.title || entry.mediaId;
      if (!window.confirm(`Delete the currently-playing entry "${label}"?`)) return;
    }
    setScheduleError("");
    setScheduleNotice("");
    deleteQueueRef.current.add(entryId);
    setDeletingEntries((prev) => new Set(prev).add(entryId));
    if (deleteTimerRef.current !== null) {
      window.clearTimeout(deleteTimerRef.current);
    }
    deleteTimerRef.current = window.setTimeout(() => {
      deleteTimerRef.current = null;
      void flushDeleteQueue();
    }, 100);
  }

  async function refreshScheduleAfterMutation(message?: string) {
    if (!channel) return;
    setScheduleLoading(true);
    try {
      const data = await getChannelSchedule(channel.id, scheduleWindowStart, WINDOW_HOURS);
      setScheduleData(data);
      setSchedulePreview(null);
      setScheduleError("");
      setScheduleNotice(message || "");
    } catch (err) {
      setScheduleError(err instanceof Error ? err.message : String(err));
      setScheduleNotice("");
    } finally {
      setScheduleLoading(false);
    }
  }

  async function loadMedia(force = false) {
    if ((!force && mediaData) || mediaLoading) return;
    setMediaLoading(true);
    setMediaError("");
    try {
      if (isDraftMode) {
        const profile = draftConfig!.packageProfile;
        const candidates = await getScheduleBuilderCandidates(profile, undefined, "all");
        const byId = new Map<string, ScheduleInsertItem>();
        for (const media of candidates.media) {
          const ready = media.packageStatus === "ready" && media.packagedDurationMs != null;
          const rawMs = ready ? media.packagedDurationMs! : media.durationMs;
          const durationMs = scheduleDurationMs(rawMs);
          if (durationMs <= 0) continue;
          byId.set(media.mediaId, {
            mediaId: media.mediaId,
            title: media.title,
            path: media.path,
            schedulingGroup: media.schedulingGroup,
            durationMs,
            packagedDurationMs: ready ? durationMs : undefined,
            packageReady: ready,
            channelMember: false,
          });
        }
        setMediaData({ profile, count: byId.size, media: Array.from(byId.values()) });
      } else {
        const [channelMedia, candidates] = await Promise.all([
          getChannelMedia(channel!.id),
          getMediaPackageCandidates(channel!.packageProfile, undefined, "ready"),
        ]);
        const memberIds = new Set(channelMedia.media.map((media) => media.mediaId));
        const byId = new Map<string, ScheduleInsertItem>();
        for (const media of candidates.media) {
          if (media.packageStatus !== "ready" || media.packagedDurationMs == null) continue;
          const durationMs = scheduleDurationMs(media.packagedDurationMs);
          if (durationMs <= 0) continue;
          byId.set(media.mediaId, {
            mediaId: media.mediaId,
            title: media.title,
            path: media.path,
            schedulingGroup: media.schedulingGroup,
            durationMs,
            packagedDurationMs: durationMs,
            packageReady: true,
            channelMember: memberIds.has(media.mediaId),
          });
        }
        for (const media of channelMedia.media) {
          if (!media.packageReady) continue;
          const durationMs = scheduleDurationMs(media.packagedDurationMs ?? media.durationMs);
          if (durationMs <= 0) continue;
          byId.set(media.mediaId, {
            mediaId: media.mediaId,
            title: media.title,
            path: media.path,
            schedulingGroup: media.schedulingGroup,
            durationMs,
            packagedDurationMs: durationMs,
            packageReady: true,
            channelMember: true,
          });
        }
        setChannelMediaIds(memberIds);
        setMediaData({
          profile: candidates.profile || channel!.packageProfile,
          count: byId.size,
          media: Array.from(byId.values()),
        });
      }
    } catch (err) {
      setMediaError(err instanceof Error ? err.message : String(err));
    } finally {
      setMediaLoading(false);
    }
  }

  function openReplace(entry: ChannelSchedule["entries"][number]) {
    if (isCurrentScheduleEntry(entry, Date.now())) {
      const label = entry.title || entry.mediaId;
      if (!window.confirm(`Jump away from the currently-playing entry "${label}"?`)) return;
    }
    setEditTarget({
      mode: "jump",
      startMs: entry.startMs,
      label: `${formatDateTime(entry.startMs)} · ${entry.title || entry.mediaId}`,
    });
    void loadMedia();
  }

  function openChoose(entry: ChannelSchedule["entries"][number]) {
    const now = Date.now();
    const isCurrent = isCurrentScheduleEntry(entry, now);
    // We can insert after any entry that ends in the future — i.e. either the
    // currently-playing entry, or any entry scheduled later.
    const canInsertAfter = entry.endMs > now;
    const canInsertBefore = entry.startMs > now;
    setEditTarget({
      mode: "choose",
      entryId: entry.entryId,
      startMs: entry.startMs,
      endMs: entry.endMs,
      label: `${formatDateTime(entry.startMs)} · ${entry.title || entry.mediaId}`,
      canInsertAfter,
      canInsertBefore,
      isCurrentlyPlaying: isCurrent,
    });
  }

  function chooseInsertAfter(entryId: string, label: string) {
    setEditTarget({
      mode: "insert-after",
      afterEntryId: entryId,
      label,
    });
    void loadMedia();
  }

  function chooseInsertBefore(entryId: string, label: string) {
    setEditTarget({
      mode: "insert-before",
      beforeEntryId: entryId,
      label,
    });
    void loadMedia();
  }

  async function insertRelativeEntry(
    anchorEntryId: string,
    media: ScheduleInsertItem,
    placement: "after" | "before",
  ) {
    const now = Date.now();
    if (!channel || !scheduleData || insertBusy) return;
    const anchorEntry = scheduleData.entries.find((e) => e.entryId === anchorEntryId);
    if (!anchorEntry) {
      setScheduleError(`entry to insert ${placement} no longer exists in schedule`);
      return;
    }
    if (placement === "after" && anchorEntry.endMs <= now) {
      setScheduleError("cannot insert after entries that have already ended");
      return;
    }
    if (placement === "before" && anchorEntry.startMs <= now) {
      setScheduleError("cannot insert before entries that have already started");
      return;
    }

    if (!media.packageReady) {
      setScheduleError("media is not ready for this channel profile");
      return;
    }

    setInsertBusy(true);
    setScheduleError("");
    setScheduleNotice("");
    try {
      await ensureChannelMedia(media);
      if (placement === "after") {
        await apiInsertScheduleEntryAfter(channel.id, anchorEntryId, media.mediaId);
      } else {
        await apiInsertScheduleEntryBefore(channel.id, anchorEntryId, media.mediaId);
      }
      setEditTarget(null);
      await refreshScheduleAfterMutation(
        `inserted ${media.title || media.mediaId} ${placement} ${formatDateTime(anchorEntry.startMs)}`,
      );
    } catch (err) {
      setScheduleError(err instanceof Error ? err.message : String(err));
      setScheduleNotice("");
    } finally {
      setInsertBusy(false);
    }
  }

  function openFill(startMs: number) {
    setEditTarget({
      mode: "fill",
      startMs,
      label: formatDateTime(startMs),
    });
    void loadMedia();
  }

  async function insertMedia(media: ScheduleInsertItem) {
    if (!channel || !editTarget || !media.packageReady || insertBusy) return;
    if (editTarget.mode !== "jump" && editTarget.mode !== "fill") return;
    setInsertBusy(true);
    setScheduleError("");
    setScheduleNotice("");
    try {
      await ensureChannelMedia(media);
      const res = await apiUpsertScheduleEntry(channel.id, media.mediaId, editTarget.startMs);
      setEditTarget(null);
      clearScheduleSelection();
      await refreshScheduleAfterMutation(
        `${editTarget.mode === "jump" ? "jumped to" : "filled"} ${formatDateTime(res.startMs)} with ${media.title || media.mediaId}`,
      );
    } catch (err) {
      setScheduleError(err instanceof Error ? err.message : String(err));
      setScheduleNotice("");
    } finally {
      setInsertBusy(false);
    }
  }

  async function ensureChannelMedia(media: Pick<ScheduleInsertItem, "mediaId" | "channelMember">) {
    if (!channel || media.channelMember || channelMediaIds.has(media.mediaId)) return;
    try {
      await apiAddChannelMedia(channel.id, media.mediaId);
    } catch (err) {
      if (!isAlreadyChannelMemberError(err)) throw err;
    }
    setChannelMediaIds((prev) => new Set(prev).add(media.mediaId));
    setMediaData((prev) => prev
      ? {
          ...prev,
          media: prev.media.map((item) =>
            item.mediaId === media.mediaId ? { ...item, channelMember: true } : item
          ),
        }
      : prev);
  }

  function isAlreadyChannelMemberError(err: unknown) {
    return typeof err === "object" && err !== null && (err as { code?: string }).code === "already_member";
  }

  function selectScheduleEntry(entryId: string, index: number, options?: { range?: boolean; additive?: boolean }) {
    setSelectedRangeStarts((prev) => {
      if (options?.range && lastSelectedEntryId && scheduleData) {
        const anchor = scheduleData.entries.findIndex((entry) => entry.entryId === lastSelectedEntryId);
        if (anchor !== -1) {
          const from = Math.min(anchor, index);
          const to = Math.max(anchor, index);
          const next = options.additive ? new Set(prev) : new Set<string>();
          for (const entry of scheduleData.entries.slice(from, to + 1)) {
            next.add(entry.entryId);
          }
          return next;
        }
      }
      const next = new Set(prev);
      if (next.has(entryId)) next.delete(entryId);
      else next.add(entryId);
      return next;
    });
    setLastSelectedEntryId(entryId);
  }

  function clearScheduleSelection() {
    setSelectedRangeStarts(new Set());
    setLastSelectedEntryId(null);
  }

  async function deleteSelectedEntries() {
    if (!channel || !scheduleData || selectedRangeStarts.size === 0 || rangeBusy) return;
    const selectedEntries = scheduleData.entries.filter((entry) => selectedRangeStarts.has(entry.entryId));
    if (selectedEntries.length === 0) return;
    const count = selectedEntries.length;
    if (!window.confirm(`Delete ${count} selected ${count === 1 ? "entry" : "entries"}?`)) return;

    setRangeBusy(true);
    setScheduleError("");
    setScheduleNotice("");
    try {
      for (const entry of selectedEntries) {
        await apiDeleteScheduleEntry(channel.id, entry.entryId);
      }
      clearScheduleSelection();
      await refreshScheduleAfterMutation(`deleted ${count} selected ${count === 1 ? "entry" : "entries"}`);
    } catch (err) {
      setScheduleError(err instanceof Error ? err.message : String(err));
      setScheduleNotice("");
    } finally {
      setRangeBusy(false);
    }
  }

  function beginScheduleEdit() {
    if (!scheduleData || scheduleData.entries.length === 0) return;
    setScheduleError("");
    setScheduleNotice("");
    setSchedulePreview(null);
    setEditTarget(null);
    setSelectedRangeStarts(new Set());
    setLastSelectedEntryId(null);
    const now = Date.now();
    const currentEntry = scheduleData.entries.find((entry) => isCurrentScheduleEntry(entry, now));
    const fromMs = currentEntry ? currentEntry.endMs : scheduleData.entries[0].startMs;
    const editableEntries = scheduleData.entries.filter((entry) => entry.startMs >= fromMs);
    setScheduleDraft(
      editableEntries.map((entry) => ({
        ...entry,
        draftId: entry.entryId,
      })),
    );
    setScheduleDraftWindow({
      fromMs,
      toMs: scheduleData.toMs,
    });
    setScheduleDraftUndo(null);
    setScheduleEditMode(true);
  }

  function beginPreviewEdit() {
    if (!schedulePreview || schedulePreview.entries.length === 0) return;
    setScheduleError("");
    setScheduleNotice("");
    setEditTarget(null);
    setSelectedRangeStarts(new Set());
    setLastSelectedEntryId(null);
    setScheduleDraft(
      schedulePreview.entries.map((entry, index) => ({
        ...entry,
        draftId: entry.entryId || `preview-${entry.mediaId}-${entry.startMs}-${index}`,
      })),
    );
    setScheduleDraftUndo(scheduleEditMode ? scheduleDraft : null);
    setScheduleDraftWindow({
      fromMs: schedulePreview.fromMs,
      toMs: schedulePreview.toMs,
    });
    setSchedulePreview(null);
    setScheduleEditMode(true);
    setScheduleNotice("loaded preview into draft");
  }

  function cancelScheduleEdit() {
    setScheduleEditMode(false);
    setScheduleDraft([]);
    setScheduleDraftUndo(null);
    setScheduleDraftWindow(null);
    setDraftInsertAt(null);
    setDragIndex(null);
    setScheduleError("");
    setScheduleNotice("");
  }

  function moveDraftEntry(fromIndex: number, toIndex: number) {
    applyScheduleDraftChange((prev) => {
      if (fromIndex === toIndex || fromIndex < 0 || toIndex < 0 || fromIndex >= prev.length || toIndex >= prev.length) {
        return prev;
      }
      const next = [...prev];
      const [item] = next.splice(fromIndex, 1);
      next.splice(toIndex, 0, item);
      return next;
    });
  }

  function removeDraftEntry(index: number) {
    applyScheduleDraftChange((prev) => prev.filter((_, i) => i !== index));
  }

  function clearScheduleDraft() {
    applyScheduleDraftChange(() => []);
  }

  function appendDraftEntry(media: ScheduleInsertItem) {
    const draftId = makeDraftInsertId(media.mediaId);
    const durationMs = scheduleDurationMs(media.packagedDurationMs ?? media.durationMs);
    if (durationMs <= 0) {
      setScheduleError("media duration is shorter than the schedule segment grid");
      setScheduleNotice("");
      return;
    }
    const newEntry: ScheduleDraftItem = {
      entryId: draftId,
      mediaId: media.mediaId,
      title: media.title,
      path: media.path,
      schedulingGroup: media.schedulingGroup,
      startMs: 0,
      endMs: durationMs,
      durationMs,
      draftId,
      requiresChannelAttach: media.channelMember === false,
    };
    applyScheduleDraftChange((prev) => {
      const next = [...prev, newEntry];
      const label = media.title || media.mediaId;
      setScheduleNotice(`added "${label}" at position ${next.length}`);
      return next;
    });
  }

  function appendDraftEntries(items: ScheduleInsertItem[]): number {
    let addedCount = 0;
    applyScheduleDraftChange((prev) => {
      const existingKeys = new Set<string>(
        prev.flatMap((e) => [e.mediaId, e.path].filter((k): k is string => !!k)),
      );
      const toAdd: ScheduleDraftItem[] = [];
      for (const media of items) {
        if (existingKeys.has(media.mediaId) || (media.path && existingKeys.has(media.path))) continue;
        const draftId = makeDraftInsertId(media.mediaId);
        const durationMs = scheduleDurationMs(media.packagedDurationMs ?? media.durationMs);
        if (durationMs <= 0) continue;
        existingKeys.add(media.mediaId);
        if (media.path) existingKeys.add(media.path);
        toAdd.push({
          entryId: draftId,
          mediaId: media.mediaId,
          title: media.title,
          path: media.path,
          schedulingGroup: media.schedulingGroup,
          startMs: 0,
          endMs: durationMs,
          durationMs,
          draftId,
          needsPackage: !media.packageReady,
          requiresChannelAttach: media.channelMember === false,
        });
      }
      addedCount = toAdd.length;
      if (toAdd.length === 0) return prev;
      setScheduleNotice(`added ${toAdd.length} entr${toAdd.length === 1 ? "y" : "ies"}`);
      return [...prev, ...toAdd];
    });
    return addedCount;
  }

  function applyScheduleDraftChange(updater: (prev: ScheduleDraftItem[]) => ScheduleDraftItem[]) {
    setScheduleDraft((prev) => {
      const next = updater(prev);
      if (next === prev) return prev;
      setScheduleDraftUndo(prev);
      return next;
    });
  }

  function undoScheduleDraftChange() {
    setScheduleDraftUndo((prev) => {
      if (!prev) return null;
      setScheduleDraft(prev);
      setScheduleNotice("undid last draft edit");
      return null;
    });
  }

  async function saveScheduleEdit() {
    if (!channel || !scheduleData || !scheduleDraftWindow || scheduleDraft.length === 0 || saveBusy) return;
    const nowMs = Date.now();
    const totalDurationMs = scheduleDraft.reduce((sum, entry) => sum + entry.durationMs, 0);
    const currentEntry = scheduleData.entries.find((entry) =>
      entry.startMs < scheduleDraftWindow.toMs &&
      scheduleDraftWindow.fromMs < entry.endMs &&
      isCurrentScheduleEntry(entry, nowMs)
    );
    if (currentEntry) {
      // Only warn if the draft would alter the slot currently on air.
      let cursor = scheduleDraftWindow.fromMs;
      let draftAtNow: ScheduleDraftItem | undefined;
      for (const entry of scheduleDraft) {
        if (cursor <= nowMs && nowMs < cursor + entry.durationMs) {
          draftAtNow = entry;
          break;
        }
        cursor += entry.durationMs;
      }
      const changed =
        !draftAtNow ||
        draftAtNow.mediaId !== currentEntry.mediaId ||
        draftAtNow.durationMs !== currentEntry.durationMs;
      if (changed) {
        const label = currentEntry.title || currentEntry.mediaId;
        if (!window.confirm(`Save schedule edits that include the currently-playing entry "${label}"?`)) return;
      }
    }
    if (scheduleDraftWindow.fromMs % SCHEDULE_SEGMENT_MS !== 0) {
      setScheduleError("draft window start is not aligned to the schedule segment grid; refresh the schedule editor and retry");
      setScheduleNotice("");
      return;
    }
    const toMs = Math.max(scheduleDraftWindow.toMs, scheduleDraftWindow.fromMs + totalDurationMs);
    setSaveBusy(true);
    setScheduleError("");
    setScheduleNotice("");
    try {
      const attachMediaIds = Array.from(new Set(
        scheduleDraft
          .filter((entry) => entry.requiresChannelAttach)
          .map((entry) => entry.mediaId),
      ));
      for (const mediaId of attachMediaIds) {
        await ensureChannelMedia({ mediaId, channelMember: false });
      }
      await apiSaveScheduleWindowOrdered(channel.id, {
        fromMs: scheduleDraftWindow.fromMs,
        toMs,
        tailMode: "preserve",
        extendTail: false,
        entries: scheduleDraft.map((entry) => ({
          mediaId: entry.mediaId,
        })),
      });
      setScheduleEditMode(false);
      setScheduleDraft([]);
      setScheduleDraftUndo(null);
      setScheduleDraftWindow(null);
      await refreshScheduleAfterMutation(`saved ${scheduleDraft.length} schedule rows`);
    } catch (err) {
      setScheduleError(err instanceof Error ? err.message : String(err));
      setScheduleNotice("");
    } finally {
      setSaveBusy(false);
    }
  }

  async function previewScheduleRebuild() {
    if (!channel || previewBusy) return;
    setPreviewBusy(true);
    setScheduleError("");
    setScheduleNotice("");
    try {
      const previewFromMs = scheduleDraftWindow?.fromMs ?? scheduleData?.entries[0]?.startMs ?? scheduleWindowStart;
      const preview = await getChannelSchedulePreview(channel.id, previewFromMs, WINDOW_HOURS);
      setSchedulePreview(preview);
    } catch (err) {
      setScheduleError(err instanceof Error ? err.message : String(err));
      setSchedulePreview(null);
    } finally {
      setPreviewBusy(false);
    }
  }

  function clearSchedulePreview() {
    setSchedulePreview(null);
  }

  async function applySchedulePreview() {
    if (!channel || !schedulePreview || schedulePreview.entries.length === 0 || applyPreviewBusy) return;
    const currentEntry = scheduleData?.entries.find((entry) => isCurrentScheduleEntry(entry, Date.now()));
    if (currentEntry) {
      const label = currentEntry.title || currentEntry.mediaId;
      if (!window.confirm(`Apply preview over the currently-playing entry "${label}"?`)) return;
    }
    setApplyPreviewBusy(true);
    setScheduleError("");
    setScheduleNotice("");
    try {
      await apiSaveScheduleWindowOrdered(channel.id, {
        fromMs: schedulePreview.fromMs,
        toMs: schedulePreview.toMs,
        tailMode: "preserve",
        entries: schedulePreview.entries.map((entry) => ({ mediaId: entry.mediaId })),
      });
      const appliedCount = schedulePreview.entries.length;
      setSchedulePreview(null);
      await refreshScheduleAfterMutation(`applied preview with ${appliedCount} generated rows`);
    } catch (err) {
      setScheduleError(err instanceof Error ? err.message : String(err));
      setScheduleNotice("");
    } finally {
      setApplyPreviewBusy(false);
    }
  }

  async function importDraftChannel() {
    if (!isDraftMode || !draftConfig || scheduleDraft.length === 0 || saveBusy) return;
    const { packageProfile, displayName, onImported } = draftConfig;
    if (!displayName.trim()) {
      setScheduleError("display name is required");
      setScheduleNotice("");
      return;
    }
    const mediaIds = [...new Set(scheduleDraft.map((e) => e.mediaId))];
    setSaveBusy(true);
    setScheduleError("");
    setScheduleNotice("creating…");
    try {
      const body = await apiCreateScheduleBuilderChannel({ displayName: displayName.trim(), packageProfile, mediaIds });
      setScheduleNotice(
        `created ${body.channelID}: ${body.scheduleEntries} entr${body.scheduleEntries === 1 ? "y" : "ies"}, queued ${body.queued.length}`,
      );
      onImported(body.channelID);
    } catch (err) {
      setScheduleError(err instanceof Error ? err.message : String(err));
      setScheduleNotice("");
    } finally {
      setSaveBusy(false);
    }
  }

  useEffect(() => {
    return () => {
      if (deleteTimerRef.current !== null) {
        window.clearTimeout(deleteTimerRef.current);
      }
      deleteQueueRef.current.clear();
      deleteProcessingRef.current = false;
    };
  }, []);

  const nowMs = Date.now();
  const windowEnd = scheduleWindowStart + WINDOW_HOURS * 3600 * 1000;
  const windowLabel = `${formatDateTime(scheduleWindowStart)} – ${formatDateTime(windowEnd)}`;
  const mutationBusy = deleteBusy || rangeBusy || insertBusy || saveBusy || applyPreviewBusy;
  const readyMedia = (mediaData?.media || []).filter((media) => media.packageReady);
  const mediaFilterLower = mediaFilter.trim().toLowerCase();
  const filteredReadyMedia = readyMedia.filter((media) => {
    if (!mediaFilterLower) return true;
    return [media.title, media.mediaId, media.schedulingGroup, media.path]
      .filter(Boolean)
      .some((value) => value!.toLowerCase().includes(mediaFilterLower));
  });
  const visibleScheduleEntries = scheduleEditMode ? scheduleDraft : (scheduleData?.entries || []);

  return {
    scheduleWindowStart,
    scheduleData,
    scheduleLoading,
    scheduleError,
    scheduleNotice,
    deletingEntries,
    selectedRangeStarts,
    rangeBusy,
    mediaData,
    mediaLoading,
    mediaError,
    editTarget,
    mediaFilter,
    setMediaFilter,
    insertBusy,
    scheduleEditMode,
    scheduleDraft,
    scheduleDraftWindow,
    draftInsertAt,
    dragIndex,
    saveBusy,
    schedulePreview,
    previewBusy,
    applyPreviewBusy,
    shiftWindow,
    jumpToNow,
    openReplace,
    openFill,
    openChoose,
    chooseInsertAfter,
    chooseInsertBefore,
    insertMedia,
    insertRelativeEntry,
    selectScheduleEntry,
    clearScheduleSelection,
    deleteSelectedEntries,
    beginScheduleEdit,
    beginPreviewEdit,
    cancelScheduleEdit,
    moveDraftEntry,
    removeDraftEntry,
    clearScheduleDraft,
    appendDraftEntry,
    appendDraftEntries,
    undoScheduleDraftChange,
    saveScheduleEdit,
    previewScheduleRebuild,
    clearSchedulePreview,
    applySchedulePreview,
    loadMedia,
    deleteEntry,
    setDraftInsertAt,
    setEditTarget,
    setDragIndex,
    nowMs,
    windowLabel,
    mutationBusy,
    filteredReadyMedia,
    visibleScheduleEntries,
    canUndoScheduleDraft: scheduleDraftUndo !== null,
    isDraftMode,
    importDraftChannel,
  };
}
