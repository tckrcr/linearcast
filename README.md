# linearcast

Always-on HLS streaming for self-programmed channels. Point it at a media library and get live TV-style playback where wall-clock time drives position — anyone who opens the stream joins at the same point in the schedule.

Channels run on a rolling schedule built from your media library. The packager pre-converts files to fMP4 segments ahead of time, so the server serves ready artifacts at request time with no live transcoding. An admin UI and JSON API let you manage channels, monitor playback state, and build schedules from a Plex or Jellyfin library.

## How It Works

`linearcast` is a single Go binary that reads channel and schedule state from SQLite, resolves pre-packaged fMP4 segments, and serves HLS over HTTP. A set of companion binaries handle the rest:

| Binary | Role |
|--------|------|
| `linearcast` | Serves HLS playlists and fMP4 segments |
| `linearcast-encoder` | Packages media files — local mode (DB direct) when `LINEARCAST_DB` is set, remote mode (HTTP claim + tar upload) otherwise |
| `linearcast-extender` | Daemon that keeps each channel's schedule horizon filled |
| `linearcast-admin` | JSON sidecar API — backs the web UI |
| `linearcast-maint` | Maintenance-only diagnostics, migrations, and repair commands |
| `linearcast-ingest` | Recovery/bootstrap media ingest and retitle tool |
| `linearcast-subtitle-audit` | Reports subtitle coverage across the library |
| `linearcast-subtitle-extract` | Backfills WebVTT subtitle sidecars without re-encoding |

A normal deploy runs all of these together in a single Docker container.

## Quick Start

You'll need Docker with Docker Compose and a media library reachable from the host. Build and run the single-container stack:

```sh
cp deploy/.env.example .env   # set host paths and admin password
docker compose build
docker compose up -d
```

Then open `/admin` to ingest media and build your first channel — see [docs/usage.md](docs/usage.md). Deployment details live in [docs/deploy.md](docs/deploy.md).

## Documentation

- [docs/api.md](docs/api.md) — playback and admin endpoints, plus Prometheus metrics
- [docs/architecture.md](docs/architecture.md) — system design, scheduler principles, and invariants
- [docs/database.md](docs/database.md) — schema and state machines
- [docs/deploy.md](docs/deploy.md) — deploy, registry images, and configuration reference
- [docs/usage.md](docs/usage.md) — ingest, building channels, scheduling, and maintenance
- [docs/tests.md](docs/tests.md) — CI, smoke scripts, and release checks

## License

Released under the [MIT License](LICENSE).
