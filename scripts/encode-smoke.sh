#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/encode-smoke.sh <host> <admin-password> [options]
       scripts/encode-smoke.sh --host <host> (--admin-password <password> | --cookie <cookie>) [options]
       scripts/encode-smoke.sh --delete-only

Submits the shared smoke media set listed in scripts/encode-smoke-media.txt for
encoding and verifies the packager worker picks up and completes the job.
Skips encode submission if all media is already packaged.

Options:
  --profile <profile>   Package profile to verify (default: h264-main-1080p)
  --timeout <seconds>   Seconds to wait for completion (default: 600)
  --force               Delete existing smoke packages first, then re-encode
  --delete-only         Delete existing smoke packages and exit

Environment:
  WEB_UI_PORT        Web UI port used with --host (default: 8080)
  SMOKE_TIMEOUT      Seconds to wait for encode completion (default: 600)

For --force and --delete-only, the local docker compose linearcast service must
be running; the reset path runs via `docker compose exec linearcast`.
EOF
}

fixture_file="scripts/encode-smoke-media.txt"
profile="h264-main-1080p"
host=""
admin_api_url="${ADMIN_API_URL:-}"
timeout_seconds="${SMOKE_TIMEOUT:-600}"
admin_password=""
cookie_header=""
force_reencode=false
delete_only=false

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

while [[ $# -gt 0 ]]; do
  case "$1" in
    -h|--help) usage; exit 0 ;;
    --host)           shift; host="${1:-}" ;;
    --admin-api-url)  shift; admin_api_url="${1:-}" ;;
    --timeout)        shift; timeout_seconds="${1:-}" ;;
    --admin-password) shift; admin_password="${1:-}" ;;
    --cookie)         shift; cookie_header="${1:-}" ;;
    --profile)        shift; profile="${1:-}" ;;
    --force)          force_reencode=true ;;
    --delete-only)    delete_only=true ;;
    *)
      if [[ -z "$host" ]]; then
        host="$1"
      elif [[ -z "$admin_password" && -z "$cookie_header" ]]; then
        admin_password="$1"
      else
        echo "Unknown argument: $1" >&2; usage >&2; exit 2
      fi
      ;;
  esac
  if [[ "$1" == "--force" || "$1" == "--delete-only" ]]; then
    shift
    continue
  fi
  if [[ -z "${1:-}" ]]; then
    echo "missing value for argument" >&2; usage >&2; exit 2
  fi
  shift
done

if [[ ! -f "$fixture_file" ]]; then
  echo "fixture file not found: $fixture_file" >&2; exit 1
fi

# Read media IDs from the shared fixture.
media_ids=()
while IFS= read -r media_id; do
  media_ids+=("$media_id")
done < <(sed -e 's/[[:space:]]*$//' -e '/^[[:space:]]*#/d' -e '/^[[:space:]]*$/d' "$fixture_file")

media_count="${#media_ids[@]}"
if [[ "$media_count" -eq 0 ]]; then
  echo "no media IDs found in $fixture_file" >&2; exit 1
fi

media_ids_json="["
for i in "${!media_ids[@]}"; do
  [[ $i -gt 0 ]] && media_ids_json+=","
  media_ids_json+="\"${media_ids[$i]}\""
done
media_ids_json+="]"

delete_encode() {
  local media_id="$1"
  docker compose exec -T linearcast \
    linearcast-admin maint delete-encode --force "$media_id"
}

require_reset_service() {
  if ! command -v docker >/dev/null 2>&1; then
    echo "docker is required for --force/--delete-only" >&2
    exit 1
  fi
  if ! docker compose ps --status running --services linearcast 2>/dev/null | grep -qx "linearcast"; then
    echo "linearcast service is not running; start it with scripts/deploy-linearcast.sh or docker compose up -d linearcast" >&2
    exit 1
  fi
}

if [[ "$delete_only" == true || "$force_reencode" == true ]]; then
  require_reset_service
  echo "resetting encode smoke packages..."
  for media_id in "${media_ids[@]}"; do
    delete_encode "$media_id"
  done
  echo ""
fi

if [[ "$delete_only" == true ]]; then
  exit 0
fi

if [[ -n "$host" ]]; then
  admin_api_url="${admin_api_url:-http://$host:${WEB_UI_PORT:-8080}}"
fi

if [[ -z "$admin_api_url" ]]; then
  echo "provide --host or --admin-api-url" >&2; usage >&2; exit 2
fi

if [[ -n "$admin_password" && -n "$cookie_header" ]]; then
  echo "provide only one of --admin-password or --cookie" >&2; exit 2
fi

if ! [[ "$timeout_seconds" =~ ^[0-9]+$ ]] || [[ "$timeout_seconds" -lt 1 ]]; then
  echo "timeout must be a positive integer: $timeout_seconds" >&2; exit 2
fi

cookie_jar="$(mktemp)"
trap 'rm -f "$cookie_jar"' EXIT

curl_auth_args=(-b "$cookie_jar")
if [[ -n "$cookie_header" ]]; then
  curl_auth_args=(-H "Cookie: $cookie_header")
fi

auth_status="$(curl -fsS "$admin_api_url/api/auth/status")"
if grep -q '"enabled":true' <<<"$auth_status"; then
  if [[ -n "$cookie_header" ]]; then
    echo "ok: using supplied admin cookie"
  elif [[ -n "$admin_password" ]]; then
    json_password="${admin_password//\\/\\\\}"
    json_password="${json_password//\"/\\\"}"
    login_body="{\"password\":\"$json_password\"}"
    curl -fsS -c "$cookie_jar" \
      -H "Content-Type: application/json" \
      -d "$login_body" \
      "$admin_api_url/api/auth/login" >/dev/null
    echo "ok: admin login"
  else
    echo "admin auth is enabled; provide --admin-password or --cookie" >&2; exit 1
  fi
fi

# Count how many of our mediaIds appear in a given package status
count_media_in_status() {
  local status="$1"
  local response found=0
  response="$(curl -fsS --max-time 10 "${curl_auth_args[@]}" \
    "$admin_api_url/api/media/package-candidates?profile=$profile&status=$status" 2>/dev/null || true)"
  for mid in "${media_ids[@]}"; do
    grep -qF "\"$mid\"" <<<"$response" 2>/dev/null && found=$((found + 1)) || true
  done
  echo "$found"
}

# Skip if everything is already packaged
ready="$(count_media_in_status ready)"
if [[ "$ready" -eq "$media_count" ]]; then
  echo "ok: media already packaged"
  exit 0
fi

# Submit encode
encode_body="{\"mediaIds\":$media_ids_json,\"profile\":\"$profile\"}"
curl -fsS "${curl_auth_args[@]}" \
  -H "Content-Type: application/json" \
  -d "$encode_body" \
  "$admin_api_url/api/media/package" >/dev/null
echo "ok: encode submitted ($media_count item(s))"

# Verify packager picks up the job within 30s
pickup_timeout=30
picked_up=false
for _ in $(seq 1 "$pickup_timeout"); do
  processing="$(count_media_in_status processing)"
  if [[ "$processing" -gt 0 ]]; then
    echo "ok: packager running ($processing item(s) processing)"
    picked_up=true
    break
  fi
  sleep 1
done
if [[ "$picked_up" != "true" ]]; then
  echo "failed: packager did not start within ${pickup_timeout}s" >&2
  exit 1
fi

# Wait for all items to finish (poll every 5s to avoid hammering during long encodes)
for _ in $(seq 1 "$((timeout_seconds / 5))"); do
  ready="$(count_media_in_status ready)"
  if [[ "$ready" -eq "$media_count" ]]; then
    echo "ok: encode complete ($media_count item(s) ready)"
    exit 0
  fi
  sleep 5
done
echo "failed: encode did not complete within ${timeout_seconds}s ($ready/$media_count ready)" >&2
exit 1
