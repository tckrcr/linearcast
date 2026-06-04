# Implemented Tests

This page lists the checks that exist today and the commands that can actually be run.

## CI

`.gitea/workflows/image.yml` runs on pushes to `main` and by manual dispatch.

The workflow currently runs tests, builds the frontend, and then publishes the
single-container image:

```sh
go test ./...
cd web-ui && npm ci && npm run build
docker build ...
docker push ...
```

Runner labels, secrets, and deployment details are Gitea/local-infra specific.

## Local Checks

Run the backend test suite:

```sh
go test ./...
```

That test suite includes ingest regression coverage for missing paths, empty
directories, unreadable directories when the host permissions make that
observable, codec-policy pass/fail cases, and missing-`ffprobe` failures under
`internal/lcingest`.

Build the admin web UI:

```sh
cd web-ui
npm ci
npm run build
```

## Release Smoke

`scripts/release-smoke.sh` is a host-run smoke check for a running stack. The
deploy script runs it after `docker compose up`.

It verifies:

1. Playback `/healthz`.
2. Admin API `/api/healthz`.
3. Web UI `/healthz`.
4. Web root `/`.
5. Admin shell `/admin`.
6. Playback `/status`.
7. Playback `/metrics` exposes `linearcast_` metrics.

```sh
scripts/release-smoke.sh localhost
```

Useful environment overrides:

```sh
SMOKE_TIMEOUT=30 scripts/release-smoke.sh localhost
# Or with explicit URLs:
scripts/release-smoke.sh --web-base-url http://127.0.0.1:8080 \
  --playback-base-url http://127.0.0.1:8888 localhost
```

This smoke test proves the deployed services are reachable and exporting basic
runtime state. It does not prove that a channel has playable media.

## Encode Smoke

`scripts/encode-smoke.sh` submits a fixed set of test media for encoding and
verifies the packager worker picks up and completes the job.

It verifies:

1. Admin login (if auth is enabled).
2. Encode job submitted via `POST /api/media/package`.
3. Packager picks up the job within 30 seconds.
4. All items reach `ready` status within the timeout (default 600s).

Skips encode submission automatically if all test media is already packaged —
safe to run repeatedly in a deployed environment without re-encoding.
The shared corpus lives in `scripts/encode-smoke-media.txt`.

```sh
scripts/encode-smoke.sh <host> <admin-password>
```

With a Docker stack:

```sh
scripts/encode-smoke.sh localhost linearcast12345
```

Useful overrides:

```sh
SMOKE_TIMEOUT=900 scripts/encode-smoke.sh localhost linearcast12345
scripts/encode-smoke.sh localhost linearcast12345 --profile h264-main-1080p
```

### Forcing a full re-encode

Use `--force` when packaging behavior changed and you want the full pipeline
instead of the skip-if-ready fast path. The reset step is container-only: it
runs `linearcast-admin maint delete-encode` inside the local `linearcast`
docker compose service, so that service must already be running.

```sh
scripts/encode-smoke.sh localhost linearcast12345 --force

# Delete only, without re-encoding
scripts/encode-smoke.sh --delete-only
```

The delete is handled by `linearcast-admin maint delete-encode`, which also
checks for future schedule entries and aborts unless `--force` is passed.

## Not Implemented Yet

These are deliberate gaps, not hidden test commands:

1. Browser automation for `/admin`.
2. CI encode tests (encode smoke runs manually; CI lacks media and a packager worker).
3. Full Plex server integration tests.
4. Long-running playback boundary tests.
