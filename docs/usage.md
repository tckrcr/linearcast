# Usage

Day-to-day operation: getting media in, building channels, and routine maintenance. Most of this happens in the admin UI at `/admin`.

## First run

After a fresh deploy the database is empty. The viewer at `/` will show a "no media" state until ingest runs — use `/admin` as the working entry point on a new server.

Use `/admin` → Library → Media sources to connect Plex, Jellyfin, or local
media directories. `linearcast-ingest` remains available only as a
recovery/bootstrap tool when the admin API is unavailable.

Once ingest finishes, open `/admin` → Schedule Builder to create channels.

On-demand channels can change package profile later from the channel page's
Change profile action. The new profile is used after the next runtime
refresh/viewer request and does not pre-queue the whole channel. Pre-encoded
channels do not expose a profile switch; create a new channel when
that encoding strategy changes.

## Add a channel

Use `/admin` → Schedule Builder for routine channel creation.

`linearcast-extender` keeps the schedule filled automatically after channel creation. New channels are picked up on the next channel refresh tick without a restart.

### Ordering modes

- `alphabetical` (default) — playlist follows the channel's linked-list order, looping
- `block` — rotates through collection labels in marathon-feel blocks; good for mixed-show channels

## Manage the schedule

Use the admin UI/API for routine schedule and channel management. The remaining `linearcast-maint` commands are limited to maintenance diagnostics: `check`, `validate-segments`, `migrate`, and `set-group`.

- `check` audits schedule *structure* over a future window (gaps, overlaps, grid alignment, missing media, out-of-bounds offsets, not-ready packages).
- `validate-segments` is a decode-level pre-flight: for each schedule entry in the window it demuxes the ready package backing it — feeding `init.mp4` byte-concatenated with the first and last `.m4s` fragment to `ffprobe -count_packets` — to confirm the m3u8 references fragments that carry decodable packets of the expected stream/codec, not just files that exist. (Counting packets, rather than reading stream metadata, is what catches a truncated or stub fragment: codec/kind are reported from `init.mp4` even for a 0-byte segment.) Report-only by default; `--requeue` marks failing packages pending for re-encode. Exits non-zero when any package fails. Example: `LINEARCAST_DB=… linearcast-maint validate-segments --hours 12 [--channel <id>] [--all] [--requeue]`.

`linearcast-extender` keeps each enabled channel scheduled to the configured
horizon. The default is 24 hours to avoid generating long stretches of repeated
entries for small channels; raise `/admin` -> Guide -> Scheduler tunables only
for deployments that need a longer guide or prebuilt schedule window.

## Maintenance

Use `/admin` → Tools → Maintenance for operator cleanup tasks: missing source media cleanup, orphan package cache cleanup, package-cache import, and SQLite database optimization (`PRAGMA optimize` plus `VACUUM`). Missing-media and orphan-package cleanup run a dry scan first and ask for confirmation before deleting rows or cache directories. Package import reattaches existing finalized package artifacts to the database without re-encoding video and rebuilds package-owned subtitle track metadata from the source. For package-size accounting on databases with existing ready packages, run `linearcast-admin maint backfill-package-bytes`; it fills `media_packages.package_bytes` from the init and segment paths already tracked in SQLite.

## Subtitles

Text subtitle streams are extracted to package-owned WebVTT sidecars during
packaging, under each package root. Bitmap subtitle streams such as PGS/VOBSUB
are probed for burn decisions and can be burned into on-demand transcode
playback when the active profile supports it. Forced text tracks are never
served as the plain per-language CC track: the CC rendition prefers full
dialogue, then SDH when that is the only non-forced choice. Forced tracks are
either burned into compatible transcode profiles or advertised in the HLS master
playlist as `FORCED=YES,AUTOSELECT=YES` renditions when they remain soft
subtitles.

Use `/admin` -> Encoding -> Subtitles to set preferred language order and
whether the top available non-forced text subtitle should be enabled by default
in the player.


## Splitting access behind a reverse proxy

By default playback and the channel list are public while `/admin` is password-gated. If you front linearcast with a reverse proxy, treat `/admin`, `/api/admin/*`, and the rest of the control-plane `/api/*` surface as private operator traffic, and keep playback/viewer routes (`/`, `/hls/*`, `/channels/*`, `/api/playable-sources`) on the public side only if that matches your deployment model.
