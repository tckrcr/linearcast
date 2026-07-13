import { ChannelArtwork } from "./ChannelArtwork";
import { formatClock, formatMs, mediaTitle, sourceNowSubtitle } from "./format";
import type { LiveSlot } from "./playbackClock";
import type { PlayableSource } from "./types";

type Props = {
  channel: PlayableSource | null;
  slot: LiveSlot;
  visible: boolean;
};

export function ChannelBanner({ channel, slot, visible }: Props) {
  if (!channel) return null;
  const nowTitle = channel.nowPlaying?.title ?? mediaTitle(slot.now);
  return (
    <div className={`tv-banner${visible ? " is-visible" : ""}`} aria-hidden={!visible}>
      <div className="tv-banner-channel">
        <ChannelArtwork
          artworkUrl={channel.artworkUrl}
          channelId={channel.id}
          displayName={channel.displayName}
          className="tv-banner-artwork"
        />
        <strong>{channel.displayName || channel.id}</strong>
        {channel.prefillMode === "on_demand" && (
          <span className="guide-channel-badge" title="On-demand — packages on tune-in">on-demand</span>
        )}
        <span className={`status status-${channel.status}`}>{channel.status}</span>
      </div>
      <div className="tv-banner-slot">
        <span className="label">now</span>
        <span className="tv-banner-title">{nowTitle}</span>
        {sourceNowSubtitle(channel) && (
          <span className="muted">{sourceNowSubtitle(channel)}</span>
        )}
        {slot.remainingMs != null && (
          <span className="muted">{formatMs(slot.remainingMs)} left</span>
        )}
      </div>
      <div className="tv-banner-slot">
        <span className="label">next</span>
        <span className="tv-banner-title">{mediaTitle(slot.next)}</span>
        {slot.next?.startMs != null && (
          <span className="muted">@ {formatClock(slot.next.startMs)}</span>
        )}
      </div>
    </div>
  );
}
