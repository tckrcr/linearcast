# Deploy

How to run linearcast on a server, plus the runtime configuration reference.

## Requirements

- Docker with Docker Compose on the host
- A media library accessible from the host, mounted by default at `/data/media`

## Run

The public `docker-compose.yml` pulls `ghcr.io/tckrcr/linearcast:latest` and
runs the single-container stack:

```sh
docker compose up -d
```

Open `http://localhost:8080/admin`, sign in with the first-run password
`linearcast`, then choose a new password when prompted. The password is stored
in SQLite after first startup.

Schema migrations run automatically on startup. The single container runs
playback, admin API, schedule extender, local encoder worker, and web UI
together under one service.

Useful server-side commands:

```sh
docker compose ps
docker compose logs -f linearcast
docker compose restart linearcast
docker compose run --rm linearcast linearcast-maint check --all
curl -fsS http://localhost:8080/api/healthz
curl -fsS http://localhost:8080/status
```

## Configuration

No `.env` file is required. The compose file has runnable defaults, and common
host-specific settings can be overridden with shell environment variables when
you run Docker Compose.

| Variable | Default | Meaning |
|----------|---------|---------|
| `LINEARCAST_DATA_DIR` | `/data/linearcast` | Host dir holding `linearcast.db`, package cache, and state |
| `LINEARCAST_MEDIA_ROOT` | `/data/media` | Host media library root, mounted read-only at `/data/media` in the container |
| `WEB_UI_PORT` | `8080` | Public nginx/web UI port published by the compose file |
| `HOST_UID` | `1000` | Container process UID for writing state/cache files |
| `HOST_GID` | `1000` | Container process GID for writing state/cache files |
| `TZ` | `UTC` | Timezone for the running processes |

Example with a custom media path and web port:

```sh
LINEARCAST_MEDIA_ROOT=/mnt/media WEB_UI_PORT=8090 docker compose up -d
```

The container runtime paths are fixed:

| Variable | Value | Meaning |
|----------|-------|---------|
| `LINEARCAST_DB` | `/data/linearcast/linearcast.db` | SQLite database path |
| `CACHE_DIR` | `/data/linearcast/cache` | Package cache path |
| `LINEARCAST_ADDR` | `:8888` | Playback listen address inside the container |

For admin UI media-server integrations, set or clear Plex tokens and Jellyfin
API keys from the Tools panel; credentials are stored in the database.
