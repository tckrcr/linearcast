import { useCallback, useEffect, useRef, useState } from "react";
import type { SubtitleSettings } from "./types";

type SubTrack = { index: number; label: string; language: string };

type Props = {
  videoRef: React.RefObject<HTMLVideoElement | null>;
  visible: boolean;
  muted: boolean;
  onMutedChange: (muted: boolean) => void;
};

type State = {
  paused: boolean;
  currentTime: number;
  seekEnd: number;
};

const initialState: State = {
  paused: true,
  currentTime: 0,
  seekEnd: 0,
};

export function PlayerControls({
  videoRef,
  visible,
  muted,
  onMutedChange,
}: Props) {
  const [state, setState] = useState<State>(initialState);
  const [fullscreen, setFullscreen] = useState(false);
  const [subTracks, setSubTracks] = useState<SubTrack[]>([]);
  const [activeSubIdx, setActiveSubIdx] = useState<number>(-1);
  const [subMenuOpen, setSubMenuOpen] = useState(false);
  const subMenuRef = useRef<HTMLDivElement>(null);
  const [subSettings, setSubSettings] = useState<SubtitleSettings | null>(null);
  const autoSelectDone = useRef(false);

  useEffect(() => {
    const video = videoRef.current;
    if (!video) return;
    function read() {
      const v = video!;
      const seekable = v.seekable;
      const seekEnd =
        seekable.length > 0 ? seekable.end(seekable.length - 1) : 0;
      setState({
        paused: v.paused,
        currentTime: v.currentTime,
        seekEnd,
      });
    }
    read();
    const events = ["play", "pause", "timeupdate", "progress", "durationchange", "seeked", "loadedmetadata"];
    events.forEach((e) => video.addEventListener(e, read));
    return () => {
      events.forEach((e) => video.removeEventListener(e, read));
    };
  }, [videoRef]);

  useEffect(() => {
    function onChange() {
      setFullscreen(Boolean(document.fullscreenElement));
    }
    document.addEventListener("fullscreenchange", onChange);
    onChange();
    return () => document.removeEventListener("fullscreenchange", onChange);
  }, []);

  useEffect(() => {
    const video = videoRef.current;
    if (!video) return;
    function sync(e?: Event) {
      const list = video!.textTracks;
      const tracks: SubTrack[] = [];
      for (let i = 0; i < list.length; i++) {
        const t = list[i];
        if (t.kind === "subtitles" || t.kind === "captions") {
          tracks.push({ index: i, label: t.label || t.language || `Track ${i + 1}`, language: t.language });
        }
      }
      setSubTracks(tracks);
      let active = -1;
      for (let i = 0; i < list.length; i++) {
        if (list[i].mode === "showing") { active = i; break; }
      }
      setActiveSubIdx(active);

      // Auto-select top-preference language on first addtrack if enabled.
      if (e?.type === "addtrack" && !autoSelectDone.current && subSettings?.subtitleAutoEnable && tracks.length > 0) {
        const prefs = subSettings.subtitleLanguagePreference;
        for (const pref of prefs) {
          const match = tracks.find(t => t.language.toLowerCase() === pref.toLowerCase());
          if (match != null) {
            for (let i = 0; i < list.length; i++) {
              list[i].mode = i === match.index ? "showing" : "hidden";
            }
            setActiveSubIdx(match.index);
            autoSelectDone.current = true;
            break;
          }
        }
      }
    }
    const list = video.textTracks;
    list.addEventListener("change", sync);
    list.addEventListener("addtrack", sync);
    list.addEventListener("removetrack", sync);
    sync();
    return () => {
      list.removeEventListener("change", sync);
      list.removeEventListener("addtrack", sync);
      list.removeEventListener("removetrack", sync);
      autoSelectDone.current = false;
    };
  }, [videoRef, subSettings]);

  useEffect(() => {
    if (!subMenuOpen) return;
    function onPointerDown(e: PointerEvent) {
      if (subMenuRef.current && !subMenuRef.current.contains(e.target as Node)) {
        setSubMenuOpen(false);
      }
    }
    document.addEventListener("pointerdown", onPointerDown);
    return () => document.removeEventListener("pointerdown", onPointerDown);
  }, [subMenuOpen]);

  useEffect(() => {
    fetch("/api/subtitle-settings")
      .then(r => r.ok ? r.json() : null)
      .then((s: SubtitleSettings | null) => { if (s) setSubSettings(s); })
      .catch(() => {});
  }, []);

  const selectSubTrack = useCallback((idx: number) => {
    const video = videoRef.current;
    if (!video) return;
    const list = video.textTracks;
    for (let i = 0; i < list.length; i++) {
      list[i].mode = i === idx ? "showing" : "hidden";
    }
    setActiveSubIdx(idx);
  }, [videoRef]);

  const togglePlay = useCallback(() => {
    const v = videoRef.current;
    if (!v) return;
    if (v.paused) v.play().catch(() => {});
    else v.pause();
  }, [videoRef]);

  const toggleFullscreen = useCallback(() => {
    if (document.fullscreenElement) {
      document.exitFullscreen().catch(() => {});
    } else {
      document.documentElement.requestFullscreen().catch(() => {});
    }
  }, []);

  return (
    <div
      className={`tv-controls${visible ? " is-visible" : ""}`}
      aria-hidden={!visible}
    >
      <div className="tv-controls-left">
        <button
          type="button"
          className="tv-controls-btn tv-controls-play"
          onClick={togglePlay}
          aria-label={state.paused ? "Play" : "Pause"}
        >
          {state.paused ? "▶" : "❚❚"}
        </button>
      </div>

      <div className="tv-controls-timeline" aria-label="Live position">
        <div className="tv-controls-timeline-bar">
          <div className="tv-controls-timeline-fill" />
          <div className="tv-controls-timeline-head" aria-hidden />
        </div>
      </div>

      <div className="tv-controls-right">
      {subTracks.length > 0 && (
        <div className="tv-controls-sub" ref={subMenuRef}>
          {subMenuOpen && (
            <div className="tv-controls-sub-menu" role="menu">
              <button
                type="button"
                role="menuitem"
                className={`tv-controls-sub-option${activeSubIdx < 0 ? " is-active" : ""}`}
                onClick={() => { selectSubTrack(-1); setSubMenuOpen(false); }}
              >
                Off
              </button>
              {subTracks.map(t => (
                <button
                  key={t.index}
                  type="button"
                  role="menuitem"
                  className={`tv-controls-sub-option${activeSubIdx === t.index ? " is-active" : ""}`}
                  onClick={() => { selectSubTrack(t.index); setSubMenuOpen(false); }}
                >
                  {t.label}
                </button>
              ))}
            </div>
          )}
          <button
            type="button"
            className={`tv-controls-btn${activeSubIdx >= 0 ? " is-active" : ""}`}
            aria-label="Subtitles"
            aria-expanded={subMenuOpen}
            aria-haspopup="menu"
            title={activeSubIdx >= 0 ? subTracks.find(t => t.index === activeSubIdx)?.label ?? "Subtitles on" : "Subtitles off"}
            onClick={() => {
              if (subTracks.length === 1) {
                if (activeSubIdx >= 0) selectSubTrack(-1);
                else selectSubTrack(subTracks[0].index);
              } else {
                setSubMenuOpen(o => !o);
              }
            }}
          >
            CC
          </button>
        </div>
      )}

      <button
        type="button"
        className="tv-controls-btn"
        onClick={() => onMutedChange(!muted)}
        aria-label={muted ? "Unmute" : "Mute"}
      >
        {muted ? "🔇" : "🔊"}
      </button>

      <button
        type="button"
        className="tv-controls-btn"
        onClick={toggleFullscreen}
        aria-label={fullscreen ? "Exit fullscreen" : "Enter fullscreen"}
      >
        ⛶
      </button>
      </div>
    </div>
  );
}
