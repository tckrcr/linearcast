# Usage

Day-to-day operation: getting media in, building channels, and routine maintenance. Most of this happens in the admin UI at `/admin`.

## First run

After a fresh deploy the database is empty. The viewer at `/` will show a "no media" state until ingest runs — use `/admin` as the working entry point on a new server.

Use the **Ingest** panel in `/admin` → Tools to ingest a media directory from the browser with a live log tail. `linearcast-ingest` remains available only as a recovery/bootstrap tool when the admin API is unavailable.

Once ingest finishes, open `/admin` → Schedule Builder to create channels and queue packaging.

## Add a channel

Use `/admin` → Schedule Builder for routine channel creation.

`linearcast-extender` keeps the schedule filled automatically after channel creation. New channels are picked up on the next channel refresh tick without a restart.

### Build a channel from a media server

Connect Plex or Jellyfin in `/admin` → Tools, then browse and queue media from the connected library through the Schedule Builder. Connection credentials are stored in the database (set via the Tools panel), not in `.env`.

### Ordering modes

- `alphabetical` (default) — playlist follows the channel's linked-list order, looping
- `block` — rotates through `scheduling_group` values in marathon-feel blocks; good for mixed-show channels

## Manage the schedule

Use the admin UI/API for routine schedule and channel management. The remaining `linearcast-maint` commands are limited to maintenance diagnostics: `check`, `migrate`, and `set-group`.

## Maintenance

Use `/admin` → Tools → Maintenance for operator cleanup tasks: missing source media cleanup, orphan package cache cleanup, and SQLite database optimization (`PRAGMA optimize` plus `VACUUM`). Missing-media and orphan-package cleanup run a dry scan first and ask for confirmation before deleting rows or cache directories.

## Subtitles

Subtitle coverage can be audited and backfilled from `/admin` → Tools. Two CLI tools cover the same ground for scripting or recovery:

- `linearcast-subtitle-audit` — reports subtitle coverage across the library for each configured preferred language.
- `linearcast-subtitle-extract` — backfills WebVTT subtitle sidecars for media that already has a ready package, without re-encoding video.

OpenSubtitles backfill requires `LINEARCAST_OPENSUBS_API_KEY` (see [deploy.md](deploy.md)).

## Splitting access behind a reverse proxy

By default playback and the channel list are public while `/admin` is password-gated. If you front linearcast with a reverse proxy and an external auth provider (Authelia/Authentik), [proxy-auth-policy.md](proxy-auth-policy.md) describes how to split the HTTP surface across trust tiers. This is an optional deployment pattern, not a required step.
