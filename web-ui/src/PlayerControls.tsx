import { useCallback, useEffect, useRef, useState } from "react";
import type { SubtitleSettings } from "./types";

type SubTrack = { index: number; label: string; language: string };
type BurnTrack = { language: string; label: string; streamIndex: number; forced: boolean };
type BurnTrackResponse = { activeLanguage?: string; tracks?: BurnTrack[] };

type Props = {
  channelID: string;
  videoRef: React.RefObject<HTMLVideoElement | null>;
  visible: boolean;
  muted: boolean;
  abrMode: "best" | "saver";
  abrAvailable: boolean;
  onMutedChange: (muted: boolean) => void;
  onAbrModeChange: (mode: "best" | "saver") => void;
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
  channelID,
  videoRef,
  visible,
  muted,
  abrMode,
  abrAvailable,
  onMutedChange,
  onAbrModeChange,
}: Props) {
  const [state, setState] = useState<State>(initialState);
  const [fullscreen, setFullscreen] = useState(false);
  const [subTracks, setSubTracks] = useState<SubTrack[]>([]);
  const [activeSubIdx, setActiveSubIdx] = useState<number>(-1);
  const [burnTracks, setBurnTracks] = useState<BurnTrack[]>([]);
  const [activeBurnLang, setActiveBurnLang] = useState<string>("");
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

  useEffect(() => {
    if (!channelID) {
      setBurnTracks([]);
      setActiveBurnLang("");
      return;
    }
    const ac = new AbortController();
    fetch(`/hls/channel/${encodeURIComponent(channelID)}/subtitles`, { cache: "no-store", signal: ac.signal })
      .then(r => r.ok ? r.json() : null)
      .then((data: BurnTrackResponse | null) => {
        setBurnTracks(data?.tracks ?? []);
        setActiveBurnLang(data?.activeLanguage ?? "");
      })
      .catch(() => {});
    return () => ac.abort();
  }, [channelID]);

  const selectSubTrack = useCallback((idx: number) => {
    const video = videoRef.current;
    if (!video) return;
    const list = video.textTracks;
    for (let i = 0; i < list.length; i++) {
      list[i].mode = i === idx ? "showing" : "hidden";
    }
    setActiveSubIdx(idx);
  }, [videoRef]);

  const setBurnSubtitle = useCallback((language: string) => {
    if (!channelID) return;
    fetch(`/hls/channel/${encodeURIComponent(channelID)}/subtitles`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ language }),
    })
      .then(r => r.ok ? r.json() : Promise.reject(new Error("subtitle update failed")))
      .then((data: BurnTrackResponse) => {
        setActiveBurnLang(data.activeLanguage ?? language);
      })
      .catch(() => {});
  }, [channelID]);

  const turnSubtitlesOff = useCallback(() => {
    selectSubTrack(-1);
    if (activeBurnLang) {
      setBurnSubtitle("");
      setActiveBurnLang("");
    }
  }, [activeBurnLang, selectSubTrack, setBurnSubtitle]);

  const selectBurnTrack = useCallback((language: string) => {
    selectSubTrack(-1);
    setActiveBurnLang(language);
    setBurnSubtitle(language);
  }, [selectSubTrack, setBurnSubtitle]);

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
      {(subTracks.length > 0 || burnTracks.length > 0) && (
        <div className="tv-controls-sub" ref={subMenuRef}>
          {subMenuOpen && (
            <div className="tv-controls-sub-menu" role="menu">
              <button
                type="button"
                role="menuitem"
                className={`tv-controls-sub-option${activeSubIdx < 0 && !activeBurnLang ? " is-active" : ""}`}
                onClick={() => { turnSubtitlesOff(); setSubMenuOpen(false); }}
              >
                Off
              </button>
              {subTracks.map(t => (
                <button
                  key={t.index}
                  type="button"
                  role="menuitem"
                  className={`tv-controls-sub-option${activeSubIdx === t.index && !activeBurnLang ? " is-active" : ""}`}
                  onClick={() => { setActiveBurnLang(""); setBurnSubtitle(""); selectSubTrack(t.index); setSubMenuOpen(false); }}
                >
                  {t.label}
                </button>
              ))}
              {burnTracks.map(t => (
                <button
                  key={`burn-${t.language}-${t.streamIndex}`}
                  type="button"
                  role="menuitem"
                  className={`tv-controls-sub-option${activeBurnLang === t.language ? " is-active" : ""}`}
                  onClick={() => { selectBurnTrack(t.language); setSubMenuOpen(false); }}
                >
                  {t.label}
                </button>
              ))}
            </div>
          )}
          <button
            type="button"
            className={`tv-controls-btn${activeSubIdx >= 0 || activeBurnLang ? " is-active" : ""}`}
            aria-label="Subtitles"
            aria-expanded={subMenuOpen}
            aria-haspopup="menu"
            title={activeBurnLang ? burnTracks.find(t => t.language === activeBurnLang)?.label ?? "Subtitles on" : activeSubIdx >= 0 ? subTracks.find(t => t.index === activeSubIdx)?.label ?? "Subtitles on" : "Subtitles off"}
            onClick={() => {
              const totalTracks = subTracks.length + burnTracks.length;
              if (totalTracks === 1) {
                if (activeSubIdx >= 0 || activeBurnLang) turnSubtitlesOff();
                else if (subTracks.length === 1) selectSubTrack(subTracks[0].index);
                else selectBurnTrack(burnTracks[0].language);
              } else {
                setSubMenuOpen(o => !o);
              }
            }}
          >
            CC
          </button>
        </div>
      )}

      {abrAvailable && (
        <button
          type="button"
          className={`tv-controls-btn${abrMode === "saver" ? " is-active" : ""}`}
          onClick={() => onAbrModeChange(abrMode === "best" ? "saver" : "best")}
          aria-label={abrMode === "saver" ? "Data Saver" : "Best Available"}
          title={abrMode === "saver" ? "Quality: Data Saver — tap for Best Available" : "Quality: Best Available — tap for Data Saver"}
        >
          {abrMode === "saver" ? "SD" : "HD"}
        </button>
      )}

      <button
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
