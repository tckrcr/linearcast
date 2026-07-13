# linearcast

Always-on HLS streaming for self-programmed channels. Point it at a media
library and get live TV-style playback where wall-clock time drives position:
anyone who opens the stream joins at the same point in the schedule.

Channels run on a rolling schedule built from your media library. The packager
pre-converts files to fMP4 segments ahead of time, so the server serves ready
artifacts at request time with no live transcoding. An admin UI and JSON API let
you manage channels, monitor playback state, and build schedules from a Plex or
Jellyfin library.

## Quick Start

You'll need Docker with Docker Compose and a media library mounted at
`/data/media` on the host:

```sh
docker compose up -d
```

Open `http://localhost:8080/admin`, sign in with the first-run password
`linearcast`, then choose a new password when prompted.

By default, linearcast stores its SQLite database and package cache under
`/data/linearcast`, serves the web UI on port `8080`, and reads media from
`/data/media`. Override those paths or the port with shell environment variables:

```sh
LINEARCAST_MEDIA_ROOT=/mnt/media WEB_UI_PORT=8090 docker compose up -d
```

Deployment details live in [docs/deploy.md](docs/deploy.md). After startup, use
`/admin` to add a Plex, Jellyfin, or local media source and build your first
channel.

## How It Works

`linearcast` is a single Go binary that reads channel and schedule state from
SQLite, resolves pre-packaged fMP4 segments, and serves HLS over HTTP. A normal
deploy runs playback, admin API, schedule extender, local encoder worker, and
web UI together in one Docker container.

## Documentation

- [docs/api.md](docs/api.md) - playback and admin endpoints, plus Prometheus metrics
- [docs/database.md](docs/database.md) - schema and state machines
- [docs/deploy.md](docs/deploy.md) - deploy and configuration reference
- [docs/usage.md](docs/usage.md) - ingest, building channels, scheduling, and maintenance

## License

Released under the [MIT License](LICENSE).
