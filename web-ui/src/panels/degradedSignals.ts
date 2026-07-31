// Presentation for the `/api/degraded` signals. Shared because two surfaces show
// them — the Overview attention list and the Tools health section — and a label
// that drifts between the two describes the same signal two ways.

export const SIGNAL_LABELS: Record<string, string> = {
  disk_pressure: "Disk pressure",
  at_capacity: "On-demand at capacity",
};

export const SIGNAL_ACTIONS: Record<string, string> = {
  disk_pressure: "Clean orphan packages to free cache space",
  at_capacity: "Stop a channel encoder to free capacity",
};

export function signalLabel(signal: string): string {
  return SIGNAL_LABELS[signal] ?? signal;
}
