#!/usr/bin/env bash
set -euo pipefail

project="linearcast-live-proxy-smoke"
timeout_seconds=120
host_port="18082"
bind_address="0.0.0.0"
fake_port="18091"
keep_on_failure=true

if [[ $# -gt 0 ]]; then
  echo "live-proxy-smoke takes no arguments; edit the config block at the top of the script" >&2
  exit 2
fi

if ! command -v docker >/dev/null 2>&1; then
  echo "docker is required" >&2
  exit 1
fi
if ! command -v go >/dev/null 2>&1; then
  echo "go is required to run the fake upstream" >&2
  exit 1
fi

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$repo_root"

export COMPOSE_PROJECT_NAME="$project"
network="${project}_default"
tmpdir="$(mktemp -d)"
env_override="$tmpdir/docker-compose.env.yml"
ports_override="$tmpdir/docker-compose.ports.yml"

server_log="$tmpdir/fake-upstream.log"
server_pid=""
job_cid=""
published_host_port=""
failed=false
declare -a compose_files=(-f docker-compose.yml -f deploy/docker-compose.ci.yml)

detect_job_container() {
  local cid=""
  cid="$(grep -oE 'containers/[0-9a-f]{64}' /proc/self/mountinfo \
    | head -1 | grep -oE '[0-9a-f]{64}')" || true
  if [[ -n "$cid" ]] && docker container inspect "$cid" >/dev/null 2>&1; then
    echo "$cid"
    return 0
  fi

  cid="$(cat /etc/hostname 2>/dev/null || true)"
  if [[ -n "$cid" ]] && docker container inspect "$cid" >/dev/null 2>&1; then
    echo "$cid"
    return 0
  fi

  return 1
}

job_cid="$(detect_job_container || true)"
if [[ -z "$job_cid" ]]; then
  port_spec="${bind_address}:"
  if [[ -n "$host_port" ]]; then
    port_spec+="$host_port"
  fi
  port_spec+=":8080"
  cat > "$ports_override" <<YAML
services:
  linearcast:
    ports:
      - "$port_spec"
      - "${bind_address}::8888"
    extra_hosts:
      - "host.docker.internal:host-gateway"
YAML
  compose_files+=(-f "$ports_override")
else
  cat > "$ports_override" <<'YAML'
services:
  linearcast:
    extra_hosts:
      - "host.docker.internal:host-gateway"
YAML
  compose_files+=(-f "$ports_override")
fi

compose() { docker compose "${compose_files[@]}" "$@"; }

cleanup() {
  local status="$?"
  if [[ "$status" -ne 0 ]]; then
    failed=true
  fi
  if [[ -n "$server_pid" ]]; then
    kill "$server_pid" 2>/dev/null || true
  fi
  if [[ "$failed" == true && "$keep_on_failure" == true ]]; then
    echo "keeping failed live-proxy smoke stack for debugging:"
    echo "  project: $project"
    echo "  temp dir: $tmpdir"
    echo "  fake upstream log: $server_log"
    if [[ -n "${web_base_url:-}" ]]; then
      echo "  url: $web_base_url"
    fi
    return
  fi
  docker network disconnect "$network" "${job_cid:-}" 2>/dev/null || true
  compose down --volumes 2>/dev/null || true
  rm -rf "$tmpdir"
}
trap cleanup EXIT

mkdir -p "$tmpdir/data" "$tmpdir/cache" "$tmpdir/media"

export LINEARCAST_DATA_DIR="$tmpdir/data"
export LINEARCAST_CACHE_DIR="$tmpdir/cache"
export LINEARCAST_MEDIA_ROOT="$tmpdir/media"
export LINEARCAST_DB="$tmpdir/data/linearcast.db"
export CACHE_DIR="$tmpdir/cache"
export LINEARCAST_ADDR=":8888"
export LINEARCAST_CLOCK_CHECK="disabled"
export LINEARCAST_ADMIN_ALLOW_NO_AUTH="true"
export TZ="UTC"
export HOST_UID
export HOST_GID
HOST_UID="$(id -u)"
HOST_GID="$(id -g)"

cat > "$env_override" <<'YAML'
services:
  linearcast:
    environment:
      LINEARCAST_DATA_DIR: "${LINEARCAST_DATA_DIR}"
      LINEARCAST_CACHE_DIR: "${LINEARCAST_CACHE_DIR}"
      LINEARCAST_MEDIA_ROOT: "${LINEARCAST_MEDIA_ROOT}"
      LINEARCAST_DB: "${LINEARCAST_DB}"
      CACHE_DIR: "${CACHE_DIR}"
      LINEARCAST_ADDR: "${LINEARCAST_ADDR}"
      LINEARCAST_CLOCK_CHECK: "${LINEARCAST_CLOCK_CHECK}"
      LINEARCAST_ADMIN_ALLOW_NO_AUTH: "${LINEARCAST_ADMIN_ALLOW_NO_AUTH}"
      TZ: "${TZ}"
YAML
compose_files+=(-f "$env_override")

wait_for_ok() {
  local url="$1"
  local label="$2"
  local last_err=""
  for _ in $(seq 1 "$timeout_seconds"); do
    if curl -fsS "$url" >/dev/null; then
      return 0
    else
      last_err="$?"
    fi
    sleep 1
  done
  echo "failed: timed out waiting for $label ($url)" >&2
  return "${last_err:-1}"
}

resolve_url() {
  local base="$1"
  local ref="$2"
  if [[ "$ref" =~ ^https?:// ]]; then
    echo "$ref"
  elif [[ "$ref" == /* ]]; then
    local scheme="${base%%://*}"
    local rest="${base#*://}"
    local authority="${rest%%/*}"
    echo "$scheme://$authority$ref"
  else
    echo "${base%/*}/$ref"
  fi
}

first_variant_ref() {
  awk 'prev && $0 !~ /^#/ { print; exit } /^#EXT-X-STREAM-INF:/ { prev=1 }' "$1" | tr -d '\r'
}

first_segment_ref() {
  awk '$0 !~ /^#/ && $0 != "" { print; exit }' "$1" | tr -d '\r'
}

map_ref() {
  sed -nE 's/.*URI="([^"]+)".*/\1/p' "$1" | head -1 | tr -d '\r'
}

assert_contains() {
  local file="$1"
  local needle="$2"
  if ! grep -qF "$needle" "$file"; then
    echo "failed: expected $file to contain $needle" >&2
    echo "--- $file ---" >&2
    cat "$file" >&2
    exit 1
  fi
}

# assert_no_root_absolute fails if a manifest emits a root-absolute playback URI.
# Those drop the /hls reverse-proxy mount prefix the client used, so they must
# never appear; child URIs are required to be relative to the manifest.
assert_no_root_absolute() {
  local file="$1"
  if grep -qE '/(channels|external)/' "$file"; then
    echo "failed: $file leaked a root-absolute playback URI (drops the /hls mount prefix)" >&2
    echo "--- $file ---" >&2
    cat "$file" >&2
    exit 1
  fi
}

# fetch_follow URL OUTFILE writes the body to OUTFILE following redirects and
# prints the effective final URL, so relative child URIs are resolved against the
# manifest's real location.
fetch_follow() {
  curl -fsSL -o "$2" -w '%{url_effective}' "$1"
}

echo "starting fake upstream on ${bind_address}:${fake_port}..."
go run ./cmd/live-proxy-smoke-server --addr "${bind_address}:${fake_port}" >"$server_log" 2>&1 &
server_pid="$!"
wait_for_ok "http://127.0.0.1:${fake_port}/external/master.m3u8" "fake upstream"

upstream_host="host.docker.internal:${fake_port}"
external_url="http://${upstream_host}/external/master.m3u8"

echo "seeding disposable DB..."
go run ./cmd/live-proxy-smoke-seed \
  --external-url "$external_url"

# Build from the current working tree every run. docker-compose.yml pins
# image: linearcast:local, so a plain `up` reuses a stale prebuilt image and the
# smoke would validate old code; --build forces the image to match HEAD.
compose up -d --build

if [[ -n "$job_cid" ]]; then
  docker network connect "$network" "$job_cid"
  web_base_url="http://linearcast:8080"
  playback_base_url="http://linearcast:8888"
else
  web_port="$(compose port linearcast 8080 | awk -F: 'END {print $NF}')"
  playback_port="$(compose port linearcast 8888 | awk -F: 'END {print $NF}')"
  if [[ -z "$web_port" || -z "$playback_port" ]]; then
    exit 1
  fi
  published_host_port="$web_port"
  web_base_url="http://127.0.0.1:$web_port"
  playback_base_url="http://127.0.0.1:$playback_port"
fi

wait_for_ok "$web_base_url/healthz" "web healthz"
wait_for_ok "$web_base_url/api/healthz" "admin healthz"
wait_for_ok "$playback_base_url/healthz" "playback healthz"
echo "ok: stack reachable at $web_base_url"

# Walk every manifest through the nginx /hls mount (web_base_url), not the raw
# playback backend, so a manifest that emits root-absolute /external or /channels
# child URIs (which drop the /hls prefix and would 404 to the SPA fallback) is
# actually exercised and caught. Children resolve against each manifest's
# effective URL after redirects.
hls_base="$web_base_url/hls"

external_master="$tmpdir/external-master.m3u8"
external_variant="$tmpdir/external-variant.m3u8"
external_master_url="$(fetch_follow "$hls_base/external/smoke-external/stream.m3u8" "$external_master")"
assert_no_root_absolute "$external_master"
external_variant_url="$(resolve_url "$external_master_url" "$(first_variant_ref "$external_master")")"
external_variant_url="$(fetch_follow "$external_variant_url" "$external_variant")"
assert_no_root_absolute "$external_variant"
external_init_url="$(resolve_url "$external_variant_url" "$(map_ref "$external_variant")")"
external_segment_url="$(resolve_url "$external_variant_url" "$(first_segment_ref "$external_variant")")"
[[ "$(curl -fsS "$external_init_url")" == "external-init" ]]
[[ "$(curl -fsS "$external_segment_url")" == "external-segment" ]]
echo "ok: external HLS proxy smoke (through /hls mount)"

echo "Live proxy smoke passed"
