#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/ci-stack-smoke.sh --project <name> [--timeout <seconds>]

Boot the linearcast stack on the (shared, Docker-out-of-Docker) CI host daemon
under an isolated compose project, run release-smoke.sh against it over the
compose network, dump stack logs on failure, and always tear the stack down.

This builds nothing: it expects the `linearcast:local` image to already exist,
so run `docker compose build` first. Both the develop test workflow and the main
publish workflow call this so the boot/serve smoke is byte-for-byte identical on
both paths.

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

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

# We write a throwaway ./.env for compose interpolation and tear it down on exit.
# Refuse to clobber a real one so a developer running this locally never loses
# their deploy config (CI runs on a fresh checkout, so there is nothing to lose).
if [[ -e .env ]]; then
  echo "refusing to overwrite existing $repo_root/.env (this script manages a throwaway CI .env)" >&2
  exit 1
fi

export COMPOSE_PROJECT_NAME="$project"
compose() { docker compose -f docker-compose.yml -f deploy/docker-compose.ci.yml "$@"; }
network="${project}_default"

tmpdir="$(mktemp -d)"
job_cid=""

cleanup() {
  docker network disconnect "$network" "${job_cid:-}" 2>/dev/null || true
  compose down --volumes 2>/dev/null || true
  rm -rf "$tmpdir" .env
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
  echo "LINEARCAST_ADMIN_PASSWORD=ci-smoke-test"
  echo "TZ=UTC"
  echo "HOST_UID=$(id -u)"
  echo "HOST_GID=$(id -g)"
} > .env

compose up -d

# This job runs inside its own container; the stack containers are siblings on
# the host daemon, so their published ports are NOT on this container's
# localhost. Join the compose network and probe the service by name instead.
job_cid="$(grep -oE 'containers/[0-9a-f]{64}' /proc/self/mountinfo \
  | head -1 | grep -oE '[0-9a-f]{64}')" || true
[ -n "$job_cid" ] || job_cid="$(cat /etc/hostname)"
docker network connect "$network" "$job_cid"

if ! scripts/release-smoke.sh --host linearcast --timeout "$timeout_seconds"; then
  echo "--- stack logs ---"
  compose logs
  exit 1
fi
