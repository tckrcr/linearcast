# Deploy

How to run linearcast on a server, plus the runtime configuration reference.

## Requirements

- Docker with Docker Compose on the host
- A media library accessible from the host (local path or NFS mount)
- Go only if you build from source (see below)

## Run the published image (recommended)

Published images live at `ghcr.io/tckrcr/linearcast` and pull anonymously — no build step, no registry login. Image tags track GitHub Releases: a `v1.2.3` release publishes `:v1.2.3` and updates `:latest`.

```sh
cp deploy/docker-compose.image.yml docker-compose.yml
cp deploy/.env.example .env
# in .env: set LINEARCAST_IMAGE (e.g. ghcr.io/tckrcr/linearcast:latest),
# your host paths, and a unique LINEARCAST_ADMIN_PASSWORD
docker compose up -d
```

The single container runs playback, admin API, schedule extender, local encoder worker, and web UI together under one service. Prefer the immutable version tag (e.g. `:v1.2.3`) over `:latest` for reproducible rollbacks — change the tag and `docker compose up -d` again.

A single `.env` beside the compose file drives everything: Docker Compose reads it automatically both for `${VAR}` interpolation (host bind paths, ports) and as the container `env_file` (runtime config). No `set -a; source` step is needed for manual `docker compose` commands.

**Bind mounts are identity-mapped.** Host paths come from `LINEARCAST_DATA_DIR`, `LINEARCAST_CACHE_DIR`, and `LINEARCAST_MEDIA_ROOT` (defaults `/data/...`), and each is mounted at the same path inside the container. Because of that, `LINEARCAST_DB` and `CACHE_DIR` must point at paths *inside* those host dirs — e.g. with `LINEARCAST_DATA_DIR=/data/linearcast`, `LINEARCAST_DB=/data/linearcast/linearcast.db` is valid. A path outside the mounts produces a SQLite `CANTOPEN` error at startup.

Schema migrations run automatically on startup — no manual init step.

Useful server-side commands:

```sh
docker compose ps
docker compose logs -f linearcast
docker compose restart linearcast
docker compose run --rm linearcast linearcast-maint check --all
curl -fsS http://localhost:8080/api/healthz
curl -fsS http://localhost:8080/status
```

## Build from source

For contributors and anyone who'd rather build locally, the repo's root `docker-compose.yml` builds the image (`build: .`). The deploy script wraps build + start + release smoke checks:

```sh
cp deploy/.env.example .env   # the script also creates it on first run, then exits so you can edit
scripts/deploy-linearcast.sh
```

## Configuration

Template: `deploy/.env.example`. A single `.env` holds everything — both the host bind paths Docker Compose interpolates and the runtime config the binaries read.

**Compose host bind paths.** Interpolated by Docker Compose into the bind mounts (identity-mapped to the same in-container path), with defaults in the compose file:

| Variable | Default | Meaning |
|----------|---------|---------|
| `LINEARCAST_DATA_DIR` | `/data/linearcast` | Host dir holding `linearcast.db` and state (read-write) |
| `LINEARCAST_CACHE_DIR` | `/data/linearcast/cache` | Host dir for the packager output cache (read-write) |
| `LINEARCAST_MEDIA_ROOT` | `/data/media` | Media library root (read-only); also read by the admin binary's local-source scanner |
| `WEB_UI_PORT` | `8080` | Public nginx/web UI port published by the compose file |
| `LINEARCAST_IMAGE` | — | Image tag for `docker-compose.image.yml` deploys only |

**Runtime config.** Read by the linearcast binaries inside the container. Because mounts are identity-mapped, the path values must sit inside the host dirs above:

| Variable | Default | Meaning |
|----------|---------|---------|
| `LINEARCAST_DB` | — | Path to `linearcast.db`, inside `LINEARCAST_DATA_DIR` (required) |
| `CACHE_DIR` | — | Packager output root, inside `LINEARCAST_CACHE_DIR` (required) |
| `LINEARCAST_ADDR` | `:8888` | Playback listen address inside the container |
| `LINEARCAST_ADMIN_PASSWORD` | — | Required single-password auth for `/admin` and protected admin APIs |
| `TZ` | — | Timezone for the running processes |
| `LINEARCAST_ADMIN_ALLOW_NO_AUTH` | `false` | Development/recovery-only escape hatch; set `true` to start `linearcast-admin` without auth |
| `LINEARCAST_ADMIN_COOKIE_SECURE` | `false` | Set `true` only when browsers access `/admin` over HTTPS |
| `PLEX_URL` | — | Optional Plex base URL seed for admin builds (DB takes precedence once saved via Admin → Tools) |
| `PLEX_PATH_MAP` | — | `plex=server` path prefix pairs |
| `JELLYFIN_URL` | — | Jellyfin base URL for admin connection setup |
| `JELLYFIN_PATH_MAP` | — | `jellyfin=server` path prefix pairs |
| `LINEARCAST_OPENSUBS_API_KEY` | — | OpenSubtitles API key for subtitle backfill tools |

Keep `.env` outside source control and readable only by the deploy user because it contains `LINEARCAST_ADMIN_PASSWORD`. Rotate the admin password by updating `.env` and restarting `linearcast-admin`, which also clears in-memory admin sessions. Do not ship a shared default password; set a unique value per deployment. Use `LINEARCAST_ADMIN_ALLOW_NO_AUTH=true` only for deliberate development or recovery starts when no admin password is available.

For admin UI media-server integrations, set or clear Plex tokens and Jellyfin API keys from the Tools panel; credentials are stored in the database, not `.env`.
