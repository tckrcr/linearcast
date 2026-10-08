# Deploy

How to run linearcast on a server, plus the runtime configuration reference.

## Requirements

- Docker with Docker Compose on the host
- A media library accessible from the host, mounted by default at `/data/media`

## Run

The `docker-compose.yml` builds the image from this repository and runs the
single-container stack:

```sh
docker compose up -d
```

The first run builds the image, which takes a few minutes. Later runs reuse it;
rebuild after pulling changes with `docker compose up -d --build`.

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

## Backup and restore

All durable state lives in one SQLite database on the mounted data volume at
`/data/linearcast/linearcast.db`. It holds channel configuration, schedules,
the media index, package state, the encoder registry, the admin password hash,
and the admin write log. Take verified snapshots and keep a copy off the data
volume.

### Back up the database

`linearcast-maint backup` writes a verified snapshot with `VACUUM INTO` and
prunes older snapshots:

```sh
docker compose run --rm linearcast linearcast-maint backup
```

The default snapshot directory is `/data/linearcast/backups` and the default
retention is 14 snapshots. Pass `--dir` to write elsewhere and `--keep` to
change retention:

```sh
docker compose run --rm linearcast \
  linearcast-maint backup --dir /data/linearcast/backups --keep 14
```

`backup` opens the live database read-only and never mutates it, so it is safe
to run while the stack serves. Schedule it from host cron or another scheduler,
and copy each snapshot off the data volume. A snapshot contains the admin
password hash and the admin write log, so protect the copy like the live
database.

### Restore the current schema

`restore` replaces the live database, so stop every linearcast service first
and start them again afterward.

1. Stop the stack:

   ```sh
   docker compose down
   ```

2. Verify and restore a snapshot:

   ```sh
   docker compose run --rm linearcast \
     linearcast-maint restore --confirm /data/linearcast/backups/linearcast-YYYYMMDD-HHMMSS.db
   ```

   The command verifies the snapshot's integrity and schema version, moves the
   current database and its `-wal`/`-shm` sidecars aside with a
   `.pre-restore-<timestamp>` suffix, copies the snapshot into place, and
   verifies the result. It refuses to run without `--confirm`.

3. Start the stack again:

   ```sh
   docker compose up -d
   ```

4. Check health and schedule integrity:

   ```sh
   docker compose run --rm linearcast linearcast-maint check --all
   curl -fsS http://localhost:8080/status
   ```

To reverse a restore, stop the stack and move the `.pre-restore-<timestamp>`
files back over the database. A pre-migration snapshot (an older schema
version) must be restored with the matching older image: image rollback and
database restore are separate operations.
