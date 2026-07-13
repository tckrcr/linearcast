import type { MediaWindow } from "./types";

// A skew-correction anchor: the server's wall clock reported in a snapshot,
// paired with the local clock reading at the moment that snapshot arrived. Used
// to estimate the on-screen wall clock when the player can't supply a
// program-date-time (native HLS, or before the first PDT-stamped fragment plays).
export type ClockAnchor = { serverNowMs: number; localAtMs: number } | null;

// The last trusted playhead reading: its program-date-time (playheadMs) and the
// wall clock at which we read it (atWallMs). Carried across ticks so a brief gap
// in playhead data extrapolates smoothly instead of snapping.
export type TrustedPlayhead = { playheadMs: number; atWallMs: number } | null;

// fallbackNowMs estimates the on-screen wall clock without a playhead: a
// skew-corrected wall clock anchored to the last poll (or raw wall clock when no
// anchor exists yet). Used before playback settles and for the stream-unavailable
// overlays, where there is no playhead to read.
export function fallbackNowMs(anchor: ClockAnchor, wallMs: number): number {
  return anchor ? anchor.serverNowMs + (wallMs - anchor.localAtMs) : wallMs;
}

// advanceClock produces the on-screen wall clock for one tick.
//
// playheadMs is the playhead's EXT-X-PROGRAM-DATE-TIME in ms, or null when the
// playhead can't be trusted yet — during startup hls.js seeks currentTime from
// the buffered-window origin forward to the live edge, so reading playingDate
// then makes a derived countdown race downward by a full window. Once we have a
// trusted reading we prefer it (it already folds lookahead, forward buffer, and
// live-edge lookback into a single value); a momentary gap extrapolates at 1x
// wall rate from the last trusted reading rather than snapping back to wall
// clock; and before any trusted reading we use the fallback.
export function advanceClock(
  prev: TrustedPlayhead,
  playheadMs: number | null,
  anchor: ClockAnchor,
  wallMs: number,
): { nowMs: number; trusted: TrustedPlayhead } {
  if (playheadMs != null) {
    return { nowMs: playheadMs, trusted: { playheadMs, atWallMs: wallMs } };
  }
  if (prev) {
    return { nowMs: prev.playheadMs + (wallMs - prev.atWallMs), trusted: prev };
  }
  return { nowMs: fallbackNowMs(anchor, wallMs), trusted: null };
}

export type LiveSlot = {
  now: MediaWindow | null;
  next: MediaWindow | null;
  remainingMs: number | null;
  // true once the on-screen clock has crossed the snapshot's current-program
  // boundary — a signal to the caller to re-poll for a fresh now/next.
  rolledPast: boolean;
};

// resolveLiveSlot maps a server snapshot (current + next, carrying absolute
// wall-clock boundaries) onto what is actually on screen at nowMs. Boundaries
// are compared against the on-screen clock rather than wall-clock now, so the
// countdown and the now/next labels track the playhead regardless of how far
// behind the live edge playback is parked.
export function resolveLiveSlot(
  current: MediaWindow | null | undefined,
  next: MediaWindow | null | undefined,
  nowMs: number,
): LiveSlot {
  const cur = current ?? null;
  const nxt = next ?? null;
  if (!cur) {
    // No scheduled program to count down (a music channel driven by nowPlaying,
    // or a gap).
    return { now: null, next: nxt, remainingMs: null, rolledPast: false };
  }
  if (nowMs < cur.endMs) {
    return {
      now: cur,
      next: nxt,
      remainingMs: Math.max(0, cur.endMs - nowMs),
      rolledPast: false,
    };
  }
  // The on-screen playhead has crossed the current program's end. Optimistically
  // roll to `next` so the guide flips exactly on the boundary; the caller
  // re-polls to repopulate the following program.
  if (nxt) {
    return {
      now: nxt,
      next: null,
      remainingMs: Math.max(0, nxt.endMs - nowMs),
      rolledPast: true,
    };
  }
  return { now: cur, next: nxt, remainingMs: 0, rolledPast: true };
}
