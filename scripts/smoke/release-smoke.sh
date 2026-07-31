#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/smoke/release-smoke.sh <host> [--timeout <seconds>]
       scripts/smoke/release-smoke.sh --base-url <url> [--timeout <seconds>]

Environment:
  WEB_UI_PORT    Public nginx port used with <host> (default: 8080)
  SMOKE_TIMEOUT  Seconds to wait for health endpoints (default: 30)
EOF
}

host=""
base_url="${BASE_URL:-}"
timeout_seconds="${SMOKE_TIMEOUT:-30}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    -h|--help)
      usage
      exit 0
      ;;
    --base-url)
      shift
      base_url="${1:-}"
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
  base_url="${base_url:-http://$host:${WEB_UI_PORT:-8080}}"
fi

if [[ -z "$base_url" ]]; then
  echo "provide a host or --base-url" >&2
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

check_status() {
  local url="$1"
  local want="$2"
  local label="$3"
  local got
  got="$(curl -sS -o /dev/null -w '%{http_code}' "$url")"
  if [[ "$got" != "$want" ]]; then
    echo "Route check failed: $label ($url) returned $got, want $want" >&2
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

wait_for_ok "$base_url/healthz" "composed backend healthz" || exit $?
check_url "$base_url/api/healthz" "admin healthz" || exit $?
check_url "$base_url/" "web root" || exit $?
check_url "$base_url/admin" "admin shell" || exit $?
check_url "$base_url/status" "playback status" || exit $?
check_url "$base_url/api/playable-sources" "public viewer metadata" || exit $?
check_status "$base_url/hls/channels/__smoke_missing__/stream.m3u8" "404" "public playback routing" || exit $?
auth_status="$(curl -fsS "$base_url/api/auth/status")"
if grep -q '"enabled":true' <<<"$auth_status"; then
  check_status "$base_url/api/status" "401" "protected admin routing" || exit $?
else
  check_status "$base_url/api/status" "200" "intentionally unauthenticated admin routing" || exit $?
fi
check_status "$base_url/api/encoder/ping" "401" "encoder bearer-token routing" || exit $?

metrics_body="$(curl -fsS "$base_url/metrics")"
if ! grep -q '^linearcast_' <<<"$metrics_body"; then
  echo "Health check failed: playback metrics do not expose project metrics" >&2
  exit 1
fi

echo "Health checks passed"
