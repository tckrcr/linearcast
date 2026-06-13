import { useCallback, useEffect, useState } from "react";
import { getScheduleBuilderSourceStatus } from "../api";

type SourceGateState = {
  hasMediaSource: boolean;
  plexConfigured: boolean;
  jellyfinConfigured: boolean;
  localSourceCount: number;
  loading: boolean;
  error: string;
  refresh: () => void;
};

export function useHasMediaSource(): SourceGateState {
  const [hasMediaSource, setHasMediaSource] = useState(false);
  const [plexConfigured, setPlexConfigured] = useState(false);
  const [jellyfinConfigured, setJellyfinConfigured] = useState(false);
  const [localSourceCount, setLocalSourceCount] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [tick, setTick] = useState(0);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    getScheduleBuilderSourceStatus()
      .then((status) => {
        if (cancelled) return;
        setHasMediaSource(status.hasMediaSource);
        setPlexConfigured(status.plexConfigured);
        setJellyfinConfigured(status.jellyfinConfigured);
        setLocalSourceCount(status.localSourceCount);
        setError("");
      })
      .catch((err) => {
        if (cancelled) return;
        setError(err instanceof Error ? err.message : String(err));
        setHasMediaSource(false);
        setPlexConfigured(false);
        setJellyfinConfigured(false);
        setLocalSourceCount(0);
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [tick]);

  const refresh = useCallback(() => setTick((value) => value + 1), []);

  return { hasMediaSource, plexConfigured, jellyfinConfigured, localSourceCount, loading, error, refresh };
}
