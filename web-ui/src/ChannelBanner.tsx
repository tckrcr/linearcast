import { ChannelArtwork } from "./ChannelArtwork";
import { formatClock, formatMs, mediaTitle, sourceNowSubtitle, sourceNowTitle } from "./format";
import type { PlayableSource } from "./types";

type Props = {
  channel: PlayableSource | null;
  visible: boolean;
};

export function ChannelBanner({ channel, visible }: Props) {
  if (!channel) return null;
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
        <span className={`status status-${channel.status}`}>{channel.status}</span>
      </div>
      <div className="tv-banner-slot">
        <span className="label">now</span>
        <span className="tv-banner-title">{sourceNowTitle(channel)}</span>
        {sourceNowSubtitle(channel) && (
          <span className="muted">{sourceNowSubtitle(channel)}</span>
        )}
        {channel.current?.remainingMs != null && (
          <span className="muted">{formatMs(channel.current.remainingMs)} left</span>
        )}
      </div>
      <div className="tv-banner-slot">
        <span className="label">next</span>
        <span className="tv-banner-title">{mediaTitle(channel.next)}</span>
        {channel.next?.startMs != null && (
          <span className="muted">@ {formatClock(channel.next.startMs)}</span>
        )}
      </div>
    </div>
  );
}
