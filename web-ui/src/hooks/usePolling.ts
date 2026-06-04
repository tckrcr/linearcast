import { useEffect, useRef } from "react";

type PollResult = void | "stop";

type UsePollingOptions = {
  enabled?: boolean;
  intervalMs: number;
  maxIntervalMs?: number;
  backoffFactor?: number;
  immediate?: boolean;
  visibleOnly?: boolean;
  resetKey?: unknown;
  task: (signal: AbortSignal) => Promise<PollResult> | PollResult;
};

export function usePolling({
  enabled = true,
  intervalMs,
  maxIntervalMs = intervalMs,
  backoffFactor = 2,
  immediate = true,
  visibleOnly = true,
  resetKey,
  task,
}: UsePollingOptions) {
  const taskRef = useRef(task);
  taskRef.current = task;

  useEffect(() => {
    if (!enabled) return;

    let stopped = false;
    let timer: number | undefined;
    let delayMs = intervalMs;
    let controller: AbortController | null = null;

    function clearTimer() {
      if (timer != null) {
        window.clearTimeout(timer);
        timer = undefined;
      }
    }

    function schedule(nextDelayMs: number) {
      clearTimer();
      timer = window.setTimeout(() => void run(), nextDelayMs);
    }

    async function run() {
      if (stopped) return;
      if (visibleOnly && document.visibilityState === "hidden") {
        schedule(intervalMs);
        return;
      }
      controller?.abort();
      controller = new AbortController();
      try {
        const result = await taskRef.current(controller.signal);
        if (stopped || controller.signal.aborted || result === "stop") return;
        delayMs = intervalMs;
      } catch (err) {
        if (stopped || controller.signal.aborted) return;
        delayMs = Math.min(maxIntervalMs, Math.max(intervalMs, delayMs * backoffFactor));
      }
      schedule(delayMs);
    }

    function onVisibilityChange() {
      if (document.visibilityState !== "visible") return;
      delayMs = intervalMs;
      clearTimer();
      void run();
    }

    if (visibleOnly) {
      document.addEventListener("visibilitychange", onVisibilityChange);
    }
    schedule(immediate ? 0 : intervalMs);

    return () => {
      stopped = true;
      clearTimer();
      controller?.abort();
      if (visibleOnly) {
        document.removeEventListener("visibilitychange", onVisibilityChange);
      }
    };
  }, [enabled, intervalMs, maxIntervalMs, backoffFactor, immediate, visibleOnly, resetKey]);
}
