import { useCallback, useEffect, useState } from "react";
import { getScheduleBuilderSourceStatus } from "../api";

type SourceGateState = {
  hasMediaSource: boolean;
  loading: boolean;
  error: string;
  refresh: () => void;
};

export function useHasMediaSource(): SourceGateState {
  const [hasMediaSource, setHasMediaSource] = useState(false);
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
        setError("");
      })
      .catch((err) => {
        if (cancelled) return;
        setError(err instanceof Error ? err.message : String(err));
        setHasMediaSource(false);
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [tick]);

  const refresh = useCallback(() => setTick((value) => value + 1), []);

  return { hasMediaSource, loading, error, refresh };
}
