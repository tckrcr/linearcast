#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/smoke/ci-stack-smoke.sh --project <name> [--timeout <seconds>]

Boot the linearcast stack under an isolated compose project, run
release-smoke.sh and the Playwright end-to-end suite against it, dump stack logs
on failure, and always tear the stack down. Containerized CI jobs probe over the
compose network; native CI jobs publish ephemeral localhost ports and probe
those.

This builds nothing: it expects the `linearcast:local` image to already exist,
and it expects the locked web dependencies to be installed. Run
`scripts/verify.sh` and `docker compose build` first. Both the develop test
workflow and the main publish workflow call this so the boot/serve smoke is
byte-for-byte identical on both paths.

Options:
  --project <name>     Compose project name. MUST be unique per concurrent run so
                       a CI job can never recreate/remove another stack's
                       containers on the shared daemon (see memory note
                       ci-shares-dev-daemon). Typically suffixed with the run id.
  --timeout <seconds>  Seconds release-smoke.sh waits per endpoint (default: 60).
EOF
}

project=""
timeout_seconds=60

while [[ $# -gt 0 ]]; do
  case "$1" in
    --project) project="${2:?missing value for --project}"; shift 2 ;;
    --timeout) timeout_seconds="${2:?missing value for --timeout}"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

if [[ -z "$project" ]]; then
  echo "--project is required" >&2
  usage >&2
  exit 2
fi

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$repo_root"

export COMPOSE_PROJECT_NAME="$project"
network="${project}_default"

tmpdir="$(mktemp -d)"
job_cid=""
ci_env="$tmpdir/linearcast.env"
ports_override="$tmpdir/docker-compose.ports.yml"
declare -a compose_files=(-f docker-compose.yml -f deploy/docker-compose.ci.yml)
export LINEARCAST_ENV_FILE="$ci_env"

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
  cat > "$ports_override" <<'YAML'
services:
  linearcast:
    ports:
      - "127.0.0.1::8080"
YAML
  compose_files+=(-f "$ports_override")
fi

compose() { docker compose --env-file "$ci_env" "${compose_files[@]}" "$@"; }

cleanup() {
  docker network disconnect "$network" "${job_cid:-}" 2>/dev/null || true
  compose down --volumes 2>/dev/null || true
  rm -rf "$tmpdir"
}
trap cleanup EXIT

mkdir -p "$tmpdir/data" "$tmpdir/cache" "$tmpdir/media"

{
  echo "LINEARCAST_DATA_DIR=$tmpdir/data"
  echo "LINEARCAST_CACHE_DIR=$tmpdir/cache"
  echo "LINEARCAST_MEDIA_ROOT=$tmpdir/media"
  echo "LINEARCAST_DB=$tmpdir/data/linearcast.db"
  echo "CACHE_DIR=$tmpdir/cache"
  echo "LINEARCAST_ADDR=:8888"
  echo "TZ=UTC"
  echo "HOST_UID=$(id -u)"
  echo "HOST_GID=$(id -g)"
} > "$ci_env"

compose up -d

if [[ -n "$job_cid" ]]; then
  # Docker-out-of-Docker CI jobs run inside a sibling container, so the stack's
  # published ports are not on this container's localhost. Join the compose
  # network and probe the service by name instead.
  docker network connect "$network" "$job_cid"
  base_url="http://linearcast:8080"
else
  web_port="$(compose port linearcast 8080 | awk -F: 'END {print $NF}')"
  if [[ -z "$web_port" ]]; then
    echo "failed to resolve CI smoke localhost ports" >&2
    exit 1
  fi
  base_url="http://127.0.0.1:$web_port"
fi

if ! scripts/smoke/release-smoke.sh \
  --base-url "$base_url" \
  --timeout "$timeout_seconds"; then
  echo "--- stack logs ---"
  compose logs
  exit 1
fi

if ! (
  cd web-ui
  PLAYWRIGHT_BASE_URL="$base_url" \
    PLAYWRIGHT_ADMIN_PASSWORD="linearcast" \
    PLAYWRIGHT_ADMIN_NEW_PASSWORD="linearcast-playwright" \
    npm run test:e2e
); then
  echo "--- stack logs ---"
  compose logs
  exit 1
fi
