import { useEffect, useState } from "react";
import type {
  AdminNow,
  ChannelSummary,
  GuideResponse,
  PlayableSourcesResponse,
  StreamProbe,
} from "../types";
import { usePolling } from "../hooks/usePolling";
import { getGuide } from "./channels";
import { getPlayableSources } from "./sources";

export function useAdminNow(intervalMs = 3000) {
  const [data, setData] = useState<AdminNow | null>(null);
  const [error, setError] = useState("");
  const [updatedAt, setUpdatedAt] = useState<number | null>(null);

  usePolling({
    intervalMs,
    maxIntervalMs: 60_000,
    task: async (signal) => {
      try {
        const response = await fetch("/api/now", {
          signal,
          cache: "no-store",
        });
        if (!response.ok) throw new Error(`admin api ${response.status}`);
        const body = (await response.json()) as AdminNow;
        setData(body);
        setError("");
        setUpdatedAt(Date.now());
      } catch (err) {
        if (signal.aborted) return;
        setError(err instanceof Error ? err.message : String(err));
        throw err;
      }
    },
  });

  return { data, error, updatedAt };
}

export function useChannelList(intervalMs = 5000) {
  const [channels, setChannels] = useState<ChannelSummary[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [tick, setTick] = useState(0);

  async function refresh(signal?: AbortSignal) {
    const response = await fetch("/api/channels", { signal, cache: "no-store" });
    if (!response.ok) throw new Error(`admin api ${response.status}`);
    const body = (await response.json()) as { channels: ChannelSummary[] };
    setChannels(body.channels || []);
    setLoaded(true);
  }

  usePolling({
    intervalMs,
    maxIntervalMs: 60_000,
    resetKey: tick,
    task: refresh,
  });

  return { channels, loaded, refresh: () => setTick((t) => t + 1) };
}

export function usePlayableSources(intervalMs = 15000) {
  const [data, setData] = useState<PlayableSourcesResponse | null>(null);
  const [error, setError] = useState("");
  const [updatedAt, setUpdatedAt] = useState<number | null>(null);

  usePolling({
    intervalMs,
    maxIntervalMs: 60_000,
    task: async (signal) => {
      try {
        const body = await getPlayableSources(signal);
        setData(body);
        setError("");
        setUpdatedAt(Date.now());
      } catch (err) {
        if (signal.aborted) return;
        setError(err instanceof Error ? err.message : String(err));
        throw err;
      }
    },
  });

  return { data, error, updatedAt };
}

// useGuide fetches the viewer-safe EPG for a time window and re-fetches when the
// window (fromMs/hours) changes. It keeps the previous data on a window change so
// the grid does not flash empty while the next window loads.
export function useGuide(fromMs: number, hours: number, intervalMs = 60_000) {
  const [data, setData] = useState<GuideResponse | null>(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);

  usePolling({
    intervalMs,
    maxIntervalMs: 5 * intervalMs,
    resetKey: `${fromMs}:${hours}`,
    task: async (signal) => {
      try {
        const body = await getGuide(fromMs, hours, signal);
        setData(body);
        setError("");
        setLoading(false);
      } catch (err) {
        if (signal.aborted) return;
        setError(err instanceof Error ? err.message : String(err));
        setLoading(false);
        throw err;
      }
    },
  });

  return { data, error, loading };
}

export function useStreamProbe(source: string) {
  const [probe, setProbe] = useState<StreamProbe>({
    status: "checking",
    detail: "probing manifest",
  });

  useEffect(() => {
    setProbe({ status: "checking", detail: "probing manifest" });
  }, [source]);

  usePolling({
    intervalMs: 3000,
    maxIntervalMs: 30_000,
    resetKey: source,
    task: async (signal) => {
      try {
        const response = await fetch(source, {
          method: "GET",
          signal,
          cache: "no-store",
        });
        if (response.ok) {
          const body = await response.text();
          if (body.startsWith("#EXTM3U")) {
            setProbe({ status: "ready", detail: `${body.split("\n").length} lines` });
            return "stop";
          }
          setProbe({ status: "waiting", detail: "manifest missing #EXTM3U" });
        } else {
          setProbe({ status: "waiting", detail: `manifest ${response.status}` });
        }
      } catch (err) {
        if (signal.aborted) return;
        setProbe({
          status: "waiting",
          detail: err instanceof Error ? err.message : String(err),
        });
        throw err;
      }
    },
  });

  return probe;
}
