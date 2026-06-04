# Proxy and auth policy

How to split Linearcast's HTTP surface across trust tiers at a reverse proxy
(Caddy/nginx) with an external auth provider (Authelia/Authentik). This is the
deployment-side contract for the schedule-builder sharing boundary described in
[ROADMAP.md](ROADMAP.md); the application enforces the same shapes in
code, but the proxy is what actually gates access between tiers.

Linearcast does not grow users or RBAC for this. The external auth provider owns
identity and group membership; Linearcast owns clear, stable route shapes that a
path policy can match.

## Backend topology

Two backend processes sit behind the web tier (see
[`deploy/nginx.single.conf`](../deploy/nginx.single.conf)):

| Upstream | Bind var | Purpose | Public-facing path prefixes |
|---|---|---|---|
| Playback server | `LINEARCAST_ADDR` (`:8888`) | HLS manifests/segments, stream status | `/hls/*` (prefix stripped to `/channel/*` and `/external/*`), `/channel/*`, `/status`, `/metrics` |
| Admin sidecar | `LINEARCAST_ADMIN_ADDR` (`:8890`) | Everything under `/api/*` | `/api/*` |

The SPA is static (`/`, `/assets/*`, `/index.html` fallback). `/schedule` and
`/admin` are client-side routes served by the SPA fallback — they are *not*
distinct backend endpoints, so a path policy on `/schedule` / `/admin` only
controls who can load the page shell. The real enforcement is on the API
prefixes those pages call.

## Trust tiers

Capabilities are additive: scheduler inherits viewer, owner inherits scheduler.

### Viewer (watch only)

The base capability needed to load the player and watch a channel.

- `/`, `/assets/*`, `/index.html` — SPA shell
- `GET /api/playable-sources` — the channel list the player tunes from
- `GET /api/guide` — viewer-safe EPG: every guide channel's status plus a
  trimmed schedule window (no filesystem paths). Powers the player's channel
  guide overlay
- `/hls/*`, `/channel/*` — manifests and segments (includes `/hls/external/*`
  for live/external channels)
- `/status` — playback health (optional; harmless read)

### Scheduler (trusted testers) — viewer plus:

- `/schedule` — schedule-builder page shell
- `/api/schedule-builder/*` — the entire narrow facade (source status, profile
  list, candidate search, show/album/group browse, and channel create). This is
  the **only** write-capable API surface a scheduler may reach.

### Owner — scheduler plus everything else:

- `/admin` — admin page shell
- `/api/admin/*` — control plane (media sources, encoders, maintenance,
  tunables, Plex/Jellyfin config)
- All remaining `/api/*` control-plane endpoints: `/api/channels*`,
  `/api/media*`, `/api/filler-assets*`, `/api/ingest*`, `/api/fs/browse`,
  `/api/package-profiles/*`, `/api/cache/*`, `/api/subtitle-settings`,
  `/api/schedule/gaps`, `/api/queue-depth`, `/api/now`, `/api/playing`
- `/api/auth/*` — login/session (only meaningful with app auth enabled)
- `/metrics` — Prometheus scrape (owner/monitoring only; never expose publicly)

## Enforcement model

When app auth is enabled (`LINEARCAST_ADMIN_PASSWORD` set), the admin sidecar
protects `/admin` and the control-plane APIs with a session cookie, and
same-origin/CSRF checks guard non-GET routes. That cookie is a single shared
password, not per-tier identity — it cannot by itself separate a scheduler from
the owner.

When app auth is **disabled** (`LINEARCAST_ADMIN_ALLOW_NO_AUTH=true`, the
proxy-auth deployment shape), there is no application gate at all. **In that
mode the reverse proxy is the only thing enforcing the boundary, so Linearcast
must be unreachable except through it.** Bind the upstreams to loopback (or an
internal network) and never publish `:8888`/`:8890` directly.

## Concrete gotchas

- **Prefix specificity.** `/api/schedule-builder/` is a prefix of `/api/`. A
  naive "deny `/api/*` for the scheduler group" also blocks the facade. The
  allow rule for `/api/schedule-builder/*` must be evaluated *before* (or as a
  more specific match than) the `/api/*` deny. Most matchers pick the longest
  match, but verify — order-sensitive engines will silently get this wrong.
- **Encoder API is machine, not human.** `/api/encoder/*` authenticates with a
  bearer token, not the SSO cookie. If the proxy enforces forward-auth on
  `/api/*`, exempt `/api/encoder/*` so remote encoders can poll/claim/upload, or
  route it through a separate listener.
- **Large uploads.** Remote encoders POST tarballs to
  `/api/encoder/jobs/{id}/complete`. Keep a high `client_max_body_size` (the
  bundled nginx uses `100g` for `/api/`) on whatever path the encoder uses.
- **Watch step for schedulers.** After creating a channel the scheduler is sent
  to `/` to watch, which needs the viewer-tier playback paths — don't scope a
  scheduler to `/schedule*` + `/api/schedule-builder/*` alone or playback breaks.
- **`/metrics` and `/status`** are on the playback server, not behind `/api/`.
  Gate `/metrics` explicitly; it won't be caught by an `/api/*` rule.

## Example: Caddy + forward-auth

Sketch only — adapt matchers and the `forward_auth` block to your provider.
`@scheduler` / `@owner` stand in for group checks the auth provider returns.

```caddyfile
example.com {
    # Machine clients: bearer-token, bypass SSO.
    @encoder path /api/encoder/*
    reverse_proxy @encoder 127.0.0.1:8890

    # Owner-only: admin shell + control plane + metrics.
    @owner path /admin /api/admin/* /api/channels* /api/media* \
                /api/filler-assets* /api/ingest* /api/fs/browse \
                /api/package-profiles/* /api/cache/* /api/subtitle-settings \
                /api/schedule/gaps /api/queue-depth /api/now /api/playing \
                /api/auth/* /metrics
    handle @owner {
        forward_auth authelia:9091 { uri /api/verify?group=owner }
        reverse_proxy 127.0.0.1:8890   # /metrics is on :8888 — split if needed
    }

    # Scheduler facade (must precede the broad /api fallthrough).
    @scheduler path /schedule /api/schedule-builder/*
    handle @scheduler {
        forward_auth authelia:9091 { uri /api/verify?group=scheduler }
        reverse_proxy 127.0.0.1:8890
    }

    # Viewer tier: playback + the reads the player needs.
    handle /api/playable-sources /api/guide { reverse_proxy 127.0.0.1:8890 }
    handle_path /hls/* { reverse_proxy 127.0.0.1:8888 }
    handle /channel/* /status { reverse_proxy 127.0.0.1:8888 }

    # SPA shell.
    handle { root * /usr/share/nginx/html; try_files {path} /index.html; file_server }
}
```
