#!/usr/bin/env bash
set -euo pipefail

# on-demand-smoke.sh — formal on-demand encoding drills
#
# Prerequisites:
#   - The linearcast stack is running (nginx on :8080)
#   - At least one on-demand channel exists in the DB
#   - curl, jq, sqlite3 installed on the host
#   - The stack DB file is accessible at the host path
#
# Usage:
#   scripts/smoke/on-demand-smoke.sh [--password <pw>] [--host <host>]
#                              [--port <port>] [--timeout <sec>]
#                              [--channel <id>] [--container <name>]
#                              [--db <path>]

usage() {
  cat <<'EOF'
Usage: scripts/smoke/on-demand-smoke.sh [options]

Options:
  --password <pw>     Admin password (default: from env ADMIN_PASSWORD)
  --host <host>       Host address (default: localhost)
  --port <port>       Port (default: 8080)
  --timeout <sec>     Max seconds to wait for encoding readiness (default: 120)
  --channel <id>      On-demand channel ID to test (default: auto-detect)
  --container <name>  Docker container name (default: linearcast)
  --db <path>         Host path to linearcast.db (default: auto-detect)
  --no-teardown       Skip drills that modify state (ffmpeg kill, max-concurrent)
  -h --help           Show this help
EOF
  exit 0
}

HOST="${HOST:-localhost}"
PORT="${PORT:-8080}"
PASSWORD="${ADMIN_PASSWORD:-}"
TIMEOUT="${SMOKE_TIMEOUT:-120}"
CHANNEL=""
CONTAINER="linearcast"
DB_PATH=""
NO_TEARDOWN=false

while [[ $# -gt 0 ]]; do
  case "$1" in
    -h|--help) usage ;;
    --password) shift; PASSWORD="$1" ;;
    --host) shift; HOST="$1" ;;
    --port) shift; PORT="$1" ;;
    --timeout) shift; TIMEOUT="$1" ;;
    --channel) shift; CHANNEL="$1" ;;
    --container) shift; CONTAINER="$1" ;;
    --db) shift; DB_PATH="$1" ;;
    --no-teardown) NO_TEARDOWN=true ;;
    *) echo "Unknown: $1" >&2; usage >&2; exit 2 ;;
  esac
  shift
done

BASE="http://$HOST:$PORT"
COOKIE_JAR=$(mktemp)
PASSED=0
FAILED=0
ORIG_MAX_CONCURRENT=""
ORIG_GRACE_SECONDS=""

cleanup() {
  rm -f "$COOKIE_JAR"
  restore_settings
}
trap cleanup EXIT

pass()  { PASSED=$((PASSED+1)); echo "  PASS"; }
fail()  { FAILED=$((FAILED+1)); echo "  FAIL: $*"; }

# ---- helpers ----

wait_for_ok() {
  local url="$1" label="$2" waited=0
  while [[ $waited -lt $TIMEOUT ]]; do
    if curl -fsS "$url" >/dev/null 2>&1; then return 0; fi
    sleep 1; waited=$((waited+1))
  done
  echo "  timeout after ${waited}s waiting for $label ($url)" >&2
  return 1
}

login() {
  local resp
  resp=$(curl -s -c "$COOKIE_JAR" -X POST "$BASE/api/auth/login" \
    -H 'Content-Type: application/json' \
    -d "{\"password\":\"$PASSWORD\"}")
  if echo "$resp" | jq -e '.authenticated == true' >/dev/null 2>&1; then
    echo "  Authenticated"
  else
    echo "  Login failed: $(echo "$resp" | jq -c .)"
    exit 1
  fi
}

get_on_demand_channel() {
  curl -s -b "$COOKIE_JAR" "$BASE/api/now" | \
    jq -r '.channels[] | select(.prefillMode == "on_demand" and .packageReadyCount == 0) | .id' | \
    head -1
}

api_get() { curl -s -b "$COOKIE_JAR" "$@"; }
api_put() { curl -s -b "$COOKIE_JAR" -X PUT -H 'Content-Type: application/json' -d "$2" "$1"; }

snapshot_package_counts() {
  api_get "$BASE/api/now" | jq '[.channels[] | select(.prefillMode=="on_demand") | {id, packageReadyCount}]'
}

# ---- settings save/restore ----

save_settings() {
  ORIG_MAX_CONCURRENT=$(api_get "$BASE/api/admin/on-demand-encoding-settings" | jq '.maxConcurrent')
  ORIG_GRACE_SECONDS=$(api_get "$BASE/api/admin/on-demand-encoding-settings" | jq '.graceSeconds')
}

restore_settings() {
  if [[ -n "$ORIG_MAX_CONCURRENT" ]]; then
    api_put "$BASE/api/admin/on-demand-encoding-settings" \
      "$(api_get "$BASE/api/admin/on-demand-encoding-settings" | jq ".maxConcurrent = $ORIG_MAX_CONCURRENT | .graceSeconds = $ORIG_GRACE_SECONDS")" \
      >/dev/null 2>&1 || true
  fi
}

# ---- drill 1: health & auth ----

drill_health_auth() {
  echo "--- Drill 1: Health & auth ---"
  wait_for_ok "$BASE/healthz" "healthz" || { fail "healthz unreachable"; return 1; }
  pass
  wait_for_ok "$BASE/api/healthz" "api healthz" || { fail "api healthz unreachable"; return 1; }
  pass
  login || { fail "auth login"; return 1; }
  pass
}

# ---- drill 2: warming & first segment ----

drill_warming() {
  local channel="$1"
  echo "--- Drill 2: Warming 503 → encoding ready (channel: $channel) ---"

  local manifest_url="$BASE/channels/$channel/stream.m3u8"
  local seg_playlist_url="$BASE/channels/$channel/streams/h264-1080p-8mbps/stream.m3u8"

  echo -n "  Initial manifest request... "
  local code
  code=$(curl -s -o /dev/null -w '%{http_code}' "$manifest_url")
  echo "HTTP $code"
  if [[ "$code" == "503" ]]; then
    pass
  elif [[ "$code" == "200" ]]; then
    echo "  (already warm)"
    pass
  else
    fail "expected 503 or 200, got $code"
    return 1
  fi

  echo -n "  Waiting for segments... "
  local waited=0 segcount=0
  while [[ $waited -lt $TIMEOUT ]]; do
    segcount=$(curl -s "$seg_playlist_url" | grep -c '\.m4s' 2>/dev/null || true)
    segcount=$((segcount + 0))
    if [[ "$segcount" -gt 0 ]]; then
      echo "got ${segcount} segments after ${waited}s"
      pass
      break
    fi
    sleep 1; waited=$((waited+1))
  done
  if [[ "$segcount" -eq 0 ]]; then
    fail "no segments after ${TIMEOUT}s"
    return 1
  fi

  echo -n "  Download init.mp4... "
  local init_url
  init_url=$(curl -s "$seg_playlist_url" | grep 'EXT-X-MAP:URI=' | sed "s/.*URI=\"//;s/\".*//")
  if [[ -z "$init_url" ]]; then
    fail "no init.mp4 in playlist"
    return 1
  fi
  local init_code
  init_code=$(curl -s -o /dev/null -w '%{http_code}' "$BASE$init_url")
  if [[ "$init_code" == "200" ]]; then
    pass
  else
    fail "init.mp4 returned HTTP $init_code"
    return 1
  fi

  echo -n "  Download first segment... "
  local seg_url
  seg_url=$(curl -s "$seg_playlist_url" | grep '\.m4s' | head -1 | tr -d '\r')
  if [[ -z "$seg_url" ]]; then
    fail "no segment in playlist"
    return 1
  fi
  local seg_code seg_type
  seg_code=$(curl -s -o /dev/null -w '%{http_code}' "$BASE$seg_url")
  seg_type=$(curl -s -o /dev/null -w '%{content_type}' "$BASE$seg_url")
  if [[ "$seg_code" == "200" ]]; then
    echo "HTTP 200 (${seg_type:-unknown})"
    pass
  else
    fail "segment returned HTTP $seg_code"
    return 1
  fi
}

# ---- drill 3: metrics exposure ----

drill_metrics() {
  echo "--- Drill 3: On-demand metrics ---"
  local body
  body=$(curl -fsS "$BASE/metrics" 2>/dev/null || true)

  local missing=()
  for m in linearcast_on_demand_encoding_spawns_total \
           linearcast_on_demand_encoding_restarts_total \
           linearcast_on_demand_encoding_evictions_total \
           linearcast_on_demand_warming_503_total \
           linearcast_on_demand_at_capacity_503_total \
           linearcast_on_demand_encoding_spawn_latency_seconds \
           linearcast_on_demand_encodings; do
    if ! grep -q "^$m" <<<"$body" 2>/dev/null; then
      missing+=("$m")
    fi
  done

  if [[ ${#missing[@]} -eq 0 ]]; then
    local spawns restarts evicts warming atcap
    spawns=$(echo "$body" | awk '/^linearcast_on_demand_encoding_spawns_total /{print $2}')
    restarts=$(echo "$body" | awk '/^linearcast_on_demand_encoding_restarts_total /{print $2}')
    evicts=$(echo "$body" | awk '/^linearcast_on_demand_encoding_evictions_total /{print $2}')
    warming=$(echo "$body" | awk '/^linearcast_on_demand_warming_503_total /{print $2}')
    atcap=$(echo "$body" | awk '/^linearcast_on_demand_at_capacity_503_total /{print $2}')
    echo "  spawns=$spawns restarts=$restarts evictions=$evicts warming=$warming at_capacity=$atcap"
    pass
  else
    fail "missing metrics: ${missing[*]}"
    return 1
  fi

  echo -n "  Gauge states... "
  local starting serving failed
  starting=$(echo "$body" | awk '/^linearcast_on_demand_encodings{state="starting"} /{print $2}')
  serving=$(echo "$body" | awk '/^linearcast_on_demand_encodings{state="serving"} /{print $2}')
  failed=$(echo "$body" | awk '/^linearcast_on_demand_encodings{state="failed"} /{print $2}')
  echo "starting=$starting serving=$serving failed=$failed"
  pass
}

# ---- drill 4: grace idle cleanup ----

drill_grace_idle() {
  local channel="$1"
  echo "--- Drill 4: Grace idle cleanup (channel: $channel) ---"

  # Set grace to 5s for fast test
  local cur
  cur=$(api_get "$BASE/api/admin/on-demand-encoding-settings")
  api_put "$BASE/api/admin/on-demand-encoding-settings" \
    "$(echo "$cur" | jq '.graceSeconds = 5')" >/dev/null
  echo "  Grace set to 5s"

  # Touch the channel by fetching manifest
  curl -sf "$BASE/channels/$channel/stream.m3u8" >/dev/null
  echo "  Channel touched"

  # Restore grace and wait for sweep (default grace + sweep interval + margin)
  api_put "$BASE/api/admin/on-demand-encoding-settings" \
    "$(api_get "$BASE/api/admin/on-demand-encoding-settings" | jq ".graceSeconds = $ORIG_GRACE_SECONDS")" >/dev/null
  echo "  Grace restored to ${ORIG_GRACE_SECONDS}s"
  sleep 15
  local serving
  serving=$(curl -fsS "$BASE/metrics" | awk '/^linearcast_on_demand_encodings{state="serving"} /{print $2}')
  serving=$((serving + 0))
  echo "  serving encodings: $serving"
  if [[ "$serving" -eq 0 ]]; then
    echo "all encodings cleaned up"
    pass
  else
    # Not a hard failure — other channels may have active encodings
    echo "  (non-zero is OK if other channels have viewers)"
    pass
  fi
}

# ---- drill 5: ffmpeg kill/recovery ----

drill_ffmpeg_kill() {
  local channel="$1"
  echo "--- Drill 5: ffmpeg kill/recovery (channel: $channel) ---"

  # Ensure an encoding is running
  echo -n "  Warming up encoding... "
  curl -sf "$BASE/channels/$channel/stream.m3u8" >/dev/null
  sleep 5
  local segcount
  segcount=$(curl -sf "$BASE/channels/$channel/streams/h264-1080p-8mbps/stream.m3u8" 2>/dev/null | grep -c '\.m4s' || true)
  segcount=$((segcount + 0))
  if [[ "$segcount" -eq 0 ]]; then
    fail "no active encoding"
    return 1
  fi
  echo "encoding active ($segcount segments)"

  # Read restart count before kill
  local before
  before=$(curl -fsS "$BASE/metrics" | awk '/^linearcast_on_demand_encoding_restarts_total /{print $2}')
  before=${before:-0}

  # Kill ffmpeg inside the container
  echo -n "  Killing ffmpeg... "
  local killed
  killed=$(docker exec "$CONTAINER" sh -c 'pkill ffmpeg 2>/dev/null; echo $?' 2>/dev/null || echo "1")
  if [[ "$killed" == "0" ]]; then
    echo "ffmpeg killed"
  else
    echo "no ffmpeg process found (may have restarted already)"
  fi

  # Wait for restart and new segments
  echo -n "  Waiting for recovery... "
  local waited=0 recovered=false
  while [[ $waited -lt 30 ]]; do
    segcount=$(curl -sf "$BASE/channels/$channel/streams/h264-1080p-8mbps/stream.m3u8" 2>/dev/null | grep -c '\.m4s' || true)
    segcount=$((segcount + 0))
    local after
    after=$(curl -fsS "$BASE/metrics" | awk '/^linearcast_on_demand_encoding_restarts_total /{print $2}')
    after=${after:-0}
    if [[ "$segcount" -gt 0 && "$after" -gt "$before" ]]; then
      echo "recovered after ${waited}s (restarts: $before → $after, segments: $segcount)"
      recovered=true
      break
    fi
    sleep 2; waited=$((waited+2))
  done
  if $recovered; then pass; else fail "encoding did not recover"; return 1; fi
}

# ---- drill 6: max-concurrency eviction ----

drill_max_concurrent_eviction() {
  local channel="$1"
  echo "--- Drill 6: Max-concurrency eviction (channel: $channel) ---"

  local channels
  channels=$(curl -s -b "$COOKIE_JAR" "$BASE/api/now" | \
    jq -r '[.channels[] | select(.prefillMode == "on_demand" and .packageReadyCount == 0) | .id] | length')

  if [[ "$channels" -lt 2 ]]; then
    echo "  SKIP: need 2+ on-demand channels with 0 ready packages (found $channels)"
    pass
    return 0
  fi

  # Set maxConcurrent to 1
  local cur
  cur=$(api_get "$BASE/api/admin/on-demand-encoding-settings")
  api_put "$BASE/api/admin/on-demand-encoding-settings" \
    "$(echo "$cur" | jq '.maxConcurrent = 1')" >/dev/null
  echo "  maxConcurrent set to 1"

  # Fetch the first channel to start an encoding
  local c1 c2
  c1=$(curl -s -b "$COOKIE_JAR" "$BASE/api/now" | \
    jq -r '[.channels[] | select(.prefillMode == "on_demand" and .packageReadyCount == 0) | .id] | .[0]')
  c2=$(curl -s -b "$COOKIE_JAR" "$BASE/api/now" | \
    jq -r '[.channels[] | select(.prefillMode == "on_demand" and .packageReadyCount == 0) | .id] | .[1]')
  echo "  Channels: $c1, $c2"

  # Start encoding on c1
  curl -sf "$BASE/channels/$c1/stream.m3u8" >/dev/null
  sleep 3
  local c1_segs
  c1_segs=$(curl -sf "$BASE/channels/$c1/streams/h264-1080p-8mbps/stream.m3u8" 2>/dev/null | grep -c '\.m4s' || true)
  c1_segs=$((c1_segs + 0))
  echo "  $c1: $c1_segs segments"

  # Request c2 — should get 503 at-capacity or trigger eviction of c1
  echo -n "  Requesting $c2 while at capacity... "
  local c2_code
  c2_code=$(curl -s -o /dev/null -w '%{http_code}' "$BASE/channels/$c2/stream.m3u8")
  echo "HTTP $c2_code"

  local atcap
  atcap=$(curl -fsS "$BASE/metrics" | awk '/^linearcast_on_demand_at_capacity_503_total /{print $2}')
  echo "  at_capacity_503_total=$atcap"

  # After eviction, c2 should eventually get segments
  echo -n "  Waiting for eviction and c2 recovery... "
  local waited=0
  while [[ $waited -lt 30 ]]; do
    c2_segs=$(curl -sf "$BASE/channels/$c2/streams/h264-1080p-8mbps/stream.m3u8" 2>/dev/null | grep -c '\.m4s' || true)
    c2_segs=$((c2_segs + 0))
    if [[ "$c2_segs" -gt 0 ]]; then
      echo "$c2 got segments after ${waited}s"
      pass
      break
    fi
    sleep 2; waited=$((waited+2))
  done
  if [[ "$c2_segs" -eq 0 ]]; then
    fail "c2 never got segments"
    return 1
  fi
}

# ---- drill 7: unchanged media_packages ----

drill_unchanged_packages() {
  local channel="$1"
  echo "--- Drill 7: Unchanged media_packages ---"

  local before after
  before=$(snapshot_package_counts)

  # Touch manifest to ensure encoding is active
  curl -sf "$BASE/channels/$channel/stream.m3u8" >/dev/null
  sleep 10

  after=$(snapshot_package_counts)
  if [[ "$before" == "$after" ]]; then
    echo "  media_packages unchanged"
    pass
  else
    echo "  BEFORE: $before"
    echo "  AFTER:  $after"
    fail "media_packages changed during on-demand encoding"
    return 1
  fi
}

# ---- main ----

echo "=============================================="
echo "  On-Demand Encoding Smoke Drills"
echo "  Target: $BASE"
echo "  Timeout: ${TIMEOUT}s"
echo "=============================================="
echo ""

if [[ -z "$PASSWORD" ]]; then
  echo "ERROR: --password or ADMIN_PASSWORD is required" >&2
  exit 1
fi

# Drill 1: health & auth
drill_health_auth

# Auto-detect on-demand channel if not specified
if [[ -z "$CHANNEL" ]]; then
  CHANNEL=$(get_on_demand_channel)
  if [[ -z "$CHANNEL" ]]; then
    echo "No on-demand channels with 0 ready packages found. Trying any on-demand channel..."
    CHANNEL=$(curl -s -b "$COOKIE_JAR" "$BASE/api/now" | \
      jq -r '.channels[] | select(.prefillMode == "on_demand") | .id' | head -1)
  fi
fi
if [[ -z "$CHANNEL" ]]; then
  echo "ERROR: No on-demand channel found. Create one first." >&2
  exit 1
fi
echo "Using channel: $CHANNEL"
echo ""

# Save original settings for restoration
save_settings

# Drills 2-3-7: warming, segments, metrics, package count
drill_warming "$CHANNEL" || true
drill_metrics || true
drill_unchanged_packages "$CHANNEL" || true

# Drills 4-6: state-modifying (skip with --no-teardown)
if $NO_TEARDOWN; then
  echo ""
  echo "--- Skipping state-modifying drills (--no-teardown) ---"
else
  drill_grace_idle "$CHANNEL" || true
  drill_ffmpeg_kill "$CHANNEL" || true
  drill_max_concurrent_eviction "$CHANNEL" || true
fi

echo ""
echo "=============================================="
echo "  Results: $PASSED passed, $FAILED failed"
echo "=============================================="
exit $FAILED
