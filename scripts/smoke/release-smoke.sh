#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/smoke/release-smoke.sh <host> [--timeout <seconds>]
       scripts/smoke/release-smoke.sh --host <host> [--web-base-url <url>] [--playback-base-url <url>] [--admin-api-url <url>] [--timeout <seconds>]

Environment:
  WEB_UI_PORT        Web UI port used with --host (default: 8080)
  LINEARCAST_PORT    Playback port used with --host (default: 8888)
  LINEARCAST_ADMIN_PORT  Direct admin API port, only used when --admin-api-url is passed explicitly
  SMOKE_TIMEOUT      Seconds to wait for health endpoints (default: 30)
EOF
}

host=""
web_base_url="${WEB_BASE_URL:-}"
playback_base_url="${PLAYBACK_BASE_URL:-}"
admin_api_url="${ADMIN_API_URL:-}"
timeout_seconds="${SMOKE_TIMEOUT:-30}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    -h|--help)
      usage
      exit 0
      ;;
    --host)
      shift
      host="${1:-}"
      ;;
    --web-base-url)
      shift
      web_base_url="${1:-}"
      ;;
    --playback-base-url)
      shift
      playback_base_url="${1:-}"
      ;;
    --admin-api-url)
      shift
      admin_api_url="${1:-}"
      ;;
    --timeout)
      shift
      timeout_seconds="${1:-}"
      ;;
    *)
      if [[ -z "$host" ]]; then
        host="$1"
      else
        echo "Unknown argument: $1" >&2
        usage >&2
        exit 2
      fi
      ;;
  esac
  if [[ -z "${1:-}" ]]; then
    echo "missing value for argument" >&2
    usage >&2
    exit 2
  fi
  shift
done

if [[ -n "$host" ]]; then
  web_base_url="${web_base_url:-http://$host:${WEB_UI_PORT:-8080}}"
  playback_base_url="${playback_base_url:-http://$host:${LINEARCAST_PORT:-8888}}"
  admin_api_url="${admin_api_url:-$web_base_url}"
fi

if [[ -z "$web_base_url" || -z "$playback_base_url" || -z "$admin_api_url" ]]; then
  echo "provide --host or all of --web-base-url, --playback-base-url, and --admin-api-url" >&2
  usage >&2
  exit 2
fi

if ! [[ "$timeout_seconds" =~ ^[0-9]+$ ]] || [[ "$timeout_seconds" -lt 1 ]]; then
  echo "timeout must be a positive integer: $timeout_seconds" >&2
  exit 2
fi

check_url() {
  local url="$1"
  local label="$2"
  if ! curl -fsS "$url" >/dev/null; then
    echo "Health check failed: $label ($url)" >&2
    return 1
  fi
}

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
  echo "Health check failed: $label ($url)" >&2
  return "${last_err:-1}"
}

wait_for_ok "$playback_base_url/healthz" "playback healthz" || exit $?
wait_for_ok "$admin_api_url/api/healthz" "admin healthz" || exit $?
wait_for_ok "$web_base_url/healthz" "web healthz" || exit $?

check_url "$web_base_url/" "web root" || exit $?
check_url "$web_base_url/admin" "admin shell" || exit $?
check_url "$playback_base_url/status" "playback status" || exit $?

metrics_body="$(curl -fsS "$playback_base_url/metrics")"
if ! grep -q '^linearcast_' <<<"$metrics_body"; then
  echo "Health check failed: playback metrics do not expose project metrics" >&2
  exit 1
fi

echo "Health checks passed"
