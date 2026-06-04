import { useMemo, useState } from "react";

type Props = {
  artworkUrl?: string | null;
  channelId: string;
  displayName?: string;
  className?: string;
};

export function ChannelArtwork({
  artworkUrl,
  channelId,
  displayName,
  className = "",
}: Props) {
  const fallbackUrl = useMemo(
    () => defaultChannelArtworkURL(displayName || channelId),
    [channelId, displayName],
  );
  const [failedUrl, setFailedUrl] = useState<string | null>(null);
  const src = artworkUrl && artworkUrl !== failedUrl ? artworkUrl : fallbackUrl;
  const label = `${displayName || channelId} artwork`;

  return (
    <img
      className={`channel-artwork${className ? ` ${className}` : ""}`}
      src={src}
      alt={label}
      loading="lazy"
      decoding="async"
      onError={() => {
        if (src !== fallbackUrl) setFailedUrl(src);
      }}
    />
  );
}

export function defaultChannelArtworkURL(name: string) {
  const initials = channelInitials(name);
  const svg = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 160 90" role="img" aria-label="Channel artwork"><rect width="160" height="90" fill="#111820"/><path d="M0 64h160v26H0z" fill="#1f2f3d"/><path d="M0 0h160v16H0z" fill="#3c6e8f"/><text x="80" y="56" fill="#e7eaef" font-family="Arial, Helvetica, sans-serif" font-size="30" font-weight="700" text-anchor="middle">${escapeSVG(initials)}</text></svg>`;
  return `data:image/svg+xml;charset=utf-8,${encodeURIComponent(svg)}`;
}

function channelInitials(name: string) {
  const words = name
    .trim()
    .split(/[^A-Za-z0-9]+/)
    .filter(Boolean);
  if (words.length === 0) return "LC";
  return words
    .slice(0, 2)
    .map((word) => word[0])
    .join("")
    .toUpperCase();
}

function escapeSVG(value: string) {
  return value
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;");
}
