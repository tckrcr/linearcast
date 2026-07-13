import { useEffect, useRef, useState } from "react";
import type Hls from "hls.js";
import { advanceClock, fallbackNowMs, type ClockAnchor, type TrustedPlayhead } from "../playbackClock";

// playheadDateMs returns the playhead's program-date-time in ms, but only once
// playback has settled at the live position. During startup currentTime seeks
// from the buffered-window origin to the live edge; reading playingDate then
// would make a derived countdown race downward, so we withhold it until the
// media is buffered, not seeking, and past the origin.
function playheadDateMs(hls: Hls | null): number | null {
  const media = hls?.media;
  if (!hls || !media) return null;
  if (media.readyState < media.HAVE_FUTURE_DATA || media.seeking || media.currentTime < 1) {
    return null;
  }
  const pd = hls.playingDate;
  return pd ? pd.getTime() : null;
}

// usePlaybackClock returns the wall-clock instant currently on screen, re-read
// on a fixed tick so a countdown derived from it decrements smoothly at 1x. It
// tracks the playhead's program-date-time (once settled) rather than the server
// poll cadence, which is what makes "time left" advance every second instead of
// jumping in poll-sized steps.
export function usePlaybackClock(
  hlsRef: React.RefObject<Hls | null>,
  anchor: ClockAnchor,
  tickMs = 1000,
): number {
  const [nowMs, setNowMs] = useState(() => fallbackNowMs(anchor, Date.now()));
  const trustedRef = useRef<TrustedPlayhead>(null);
  useEffect(() => {
    function tick() {
      const wall = Date.now();
      const { nowMs: next, trusted } = advanceClock(
        trustedRef.current,
        playheadDateMs(hlsRef.current),
        anchor,
        wall,
      );
      trustedRef.current = trusted;
      setNowMs(next);
    }
    tick();
    const id = window.setInterval(tick, tickMs);
    return () => window.clearInterval(id);
  }, [hlsRef, anchor, tickMs]);
  return nowMs;
}
