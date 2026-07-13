import { useCallback, useEffect, useRef, useState } from "react";
import type Hls from "hls.js";
import type { Events, MediaPlaylist } from "hls.js";
import type { SubtitleSettings } from "./types";

type SubTrack = { index: number; label: string; language: string; source: "hls" | "native" };
type BurnTrack = { language: string; mode?: "burn"; label: string; streamIndex: number; forced: boolean };
type BurnTrackResponse = { activeLanguage?: string; mode?: "burn"; unavailable?: string; tracks?: BurnTrack[] };

type Props = {
  channelID: string;
  videoRef: React.RefObject<HTMLVideoElement | null>;
  visible: boolean;
  muted: boolean;
  abrMode: "best" | "saver";
  abrAvailable: boolean;
  onMutedChange: (muted: boolean) => void;
  onAbrModeChange: (mode: "best" | "saver") => void;
  onBurnSubtitleSwitch: <T>(update: () => Promise<T>) => Promise<T>;
  hlsRef: React.RefObject<Hls | null>;
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

const HLS_SUBTITLE_TRACKS_UPDATED = "hlsSubtitleTracksUpdated" as Events.SUBTITLE_TRACKS_UPDATED;
const HLS_SUBTITLE_TRACK_SWITCH = "hlsSubtitleTrackSwitch" as Events.SUBTITLE_TRACK_SWITCH;
const HLS_SUBTITLE_TRACKS_CLEARED = "hlsSubtitleTracksCleared" as Events.SUBTITLE_TRACKS_CLEARED;

export function PlayerControls({
  channelID,
  videoRef,
  visible,
  muted,
  abrMode,
  abrAvailable,
  onMutedChange,
  onAbrModeChange,
  onBurnSubtitleSwitch,
  hlsRef,
}: Props) {
  const [state, setState] = useState<State>(initialState);
  const [fullscreen, setFullscreen] = useState(false);
  const [hlsSubTracks, setHlsSubTracks] = useState<SubTrack[]>([]);
  const [nativeSubTracks, setNativeSubTracks] = useState<SubTrack[]>([]);
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
      if (hlsRef.current && hlsSubTracks.length > 0) return;
      const list = video!.textTracks;
      const tracks: SubTrack[] = [];
      for (let i = 0; i < list.length; i++) {
        const t = list[i];
        if (t.kind === "subtitles" || t.kind === "captions") {
          tracks.push({ index: i, label: t.label || t.language || `Track ${i + 1}`, language: t.language, source: "native" });
        }
      }
      setNativeSubTracks(tracks);
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
            if (!selectHlsSubtitleTrack(hlsRef.current, match)) {
              for (let i = 0; i < list.length; i++) {
                list[i].mode = i === match.index ? "showing" : "hidden";
              }
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
  }, [videoRef, subSettings, hlsRef, hlsSubTracks.length]);

  useEffect(() => {
    let attached: Hls | null = null;
    function sync() {
      const hls = attached;
      if (!hls) return;
      const tracks = hls.subtitleTracks.map((t: MediaPlaylist, index: number) => ({
        index,
        label: t.name || t.lang || `Track ${index + 1}`,
        language: t.lang || "",
        source: "hls" as const,
      }));
      setHlsSubTracks(tracks);
      setActiveSubIdx(hls.subtitleDisplay ? hls.subtitleTrack : -1);
    }
    function detach() {
      if (!attached) return;
      attached.off(HLS_SUBTITLE_TRACKS_UPDATED, sync);
      attached.off(HLS_SUBTITLE_TRACK_SWITCH, sync);
      attached.off(HLS_SUBTITLE_TRACKS_CLEARED, sync);
      attached = null;
      setHlsSubTracks([]);
    }
    function attachCurrent() {
      const current = hlsRef.current;
      if (current === attached) return;
      detach();
      if (!current) return;
      attached = current;
      attached.on(HLS_SUBTITLE_TRACKS_UPDATED, sync);
      attached.on(HLS_SUBTITLE_TRACK_SWITCH, sync);
      attached.on(HLS_SUBTITLE_TRACKS_CLEARED, sync);
      sync();
    }
    attachCurrent();
    const timer = window.setInterval(attachCurrent, 250);
    return () => {
      window.clearInterval(timer);
      detach();
    };
  }, [hlsRef]);

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
    fetch(`/hls/channels/${encodeURIComponent(channelID)}/subtitles`, { cache: "no-store", signal: ac.signal })
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
    const hls = hlsRef.current;
    const subTracks = hlsSubTracks.length > 0 ? hlsSubTracks : nativeSubTracks;
    clearTextTrackCues(video.textTracks);
    if (idx < 0) {
      if (hls) {
        hls.subtitleTrack = -1;
        hls.subtitleDisplay = false;
      }
    } else {
      const track = subTracks.find((t) => t.index === idx);
      if (track && selectHlsSubtitleTrack(hls, track)) {
        setActiveSubIdx(idx);
        return;
      }
    }
    const list = video.textTracks;
    for (let i = 0; i < list.length; i++) {
      list[i].mode = i === idx ? "showing" : "hidden";
    }
    setActiveSubIdx(idx);
  }, [hlsRef, hlsSubTracks, nativeSubTracks, videoRef]);

  const setBurnSubtitle = useCallback((language: string) => {
    if (!channelID) return;
    onBurnSubtitleSwitch(() =>
      fetch(`/hls/channels/${encodeURIComponent(channelID)}/subtitles`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ language }),
      }).then(r => r.ok ? r.json() : Promise.reject(new Error("subtitle update failed")))
    )
      .then((data: BurnTrackResponse) => {
        setActiveBurnLang(data.activeLanguage ?? language);
      })
      .catch(() => {});
  }, [channelID, onBurnSubtitleSwitch]);

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

  const preferredLanguages = subSettings?.subtitleLanguagePreference.map((l) => l.toLowerCase()) ?? [];
  const languageAllowed = useCallback((language: string) => {
    return isSubtitleLanguageAllowed(language, preferredLanguages);
  }, [preferredLanguages]);
  const subTracks = hlsSubTracks.length > 0 ? hlsSubTracks : nativeSubTracks;
  const visibleSubTracks = subTracks.filter((t) => languageAllowed(t.language));
  // Prefer WebVTT tracks when hls.js exposes them; burn-in requires a stream
  // restart and is only useful when no selectable WebVTT track exists.
  const manualBurnTracks =
    visibleSubTracks.length > 0
      ? []
      : burnTracks.filter((t) => !t.forced && languageAllowed(t.language));
  const activeManualBurnLang = manualBurnTracks.some((t) => t.language === activeBurnLang) ? activeBurnLang : "";

  const subtitlesOff = activeSubIdx < 0 && !activeManualBurnLang;

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
      {(visibleSubTracks.length > 0 || manualBurnTracks.length > 0) && (
        <div className="tv-controls-sub" ref={subMenuRef}>
          {subMenuOpen && (
            <div className="tv-controls-sub-menu" role="menu">
              <button
                type="button"
                role="menuitem"
                className={`tv-controls-sub-option${subtitlesOff ? " is-active" : ""}`}
                onClick={() => { turnSubtitlesOff(); setSubMenuOpen(false); }}
              >
                Off
              </button>
              {visibleSubTracks.map(t => (
                <button
                  key={t.index}
                  type="button"
                  role="menuitem"
                  className={`tv-controls-sub-option${activeSubIdx === t.index && !activeManualBurnLang ? " is-active" : ""}`}
                  onClick={() => { selectSubTrack(t.index); setSubMenuOpen(false); }}
                >
                  {t.label || `${t.language} WebVTT`}
                </button>
              ))}
              {manualBurnTracks.map(t => (
                <button
                  key={`burn-${t.language}-${t.streamIndex}`}
                  type="button"
                  role="menuitem"
                  className={`tv-controls-sub-option${activeManualBurnLang === t.language ? " is-active" : ""}`}
                  onClick={() => { selectBurnTrack(t.language); setSubMenuOpen(false); }}
                >
                  {t.label || `${t.language} burned-in`}
                </button>
              ))}
            </div>
          )}
          <button
            type="button"
            className={`tv-controls-btn${subtitlesOff ? "" : " is-active"}`}
            aria-label="Subtitles"
            aria-expanded={subMenuOpen}
            aria-haspopup="menu"
            title={activeManualBurnLang ? manualBurnTracks.find(t => t.language === activeManualBurnLang)?.label ?? "Subtitles on" : activeSubIdx >= 0 ? visibleSubTracks.find(t => t.index === activeSubIdx)?.label ?? "Subtitles on" : "Subtitles off"}
            onClick={() => {
              const totalTracks = visibleSubTracks.length + manualBurnTracks.length;
              if (totalTracks === 1) {
                if (!subtitlesOff) turnSubtitlesOff();
                else if (visibleSubTracks.length === 1) selectSubTrack(visibleSubTracks[0].index);
                else if (manualBurnTracks.length === 1) selectBurnTrack(manualBurnTracks[0].language);
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

function selectHlsSubtitleTrack(hls: Hls | null, track: SubTrack): boolean {
  if (!hls || track.source !== "hls") return false;
  const subtitleIndex = track.index;
  if (subtitleIndex < 0) return false;
  hls.subtitleDisplay = true;
  hls.subtitleTrack = subtitleIndex;
  return true;
}

export function isSubtitleLanguageAllowed(language: string, preferredLanguages: string[]): boolean {
  if (preferredLanguages.length === 0) return true;
  return preferredLanguages.includes(language.trim().toLowerCase());
}

function clearTextTrackCues(list: TextTrackList) {
  for (let i = 0; i < list.length; i++) {
    const track = list[i];
    const cues = track.cues;
    if (!cues) continue;
    for (let j = cues.length - 1; j >= 0; j--) {
      const cue = cues[j];
      if (cue) track.removeCue(cue);
    }
  }
}
