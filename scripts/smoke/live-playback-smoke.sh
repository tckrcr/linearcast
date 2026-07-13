#!/usr/bin/env bash
set -euo pipefail

# Config. Keep this as one obvious edit point while Phase A is being debugged.
project="linearcast-live-debug"
timeout_seconds=900
clip_seconds=36
profile="h264-1080p-8mbps"
segment_ms=2000             # packaged transport cadence; schedule grid remains 6000ms
offset_tolerance_segments=1 # allow +/-1 segment of wall-clock vs manifest skew
keep_on_failure=true
host_port="18080"
bind_address="0.0.0.0"

failed=false

if [[ $# -gt 0 ]]; then
  echo "live-playback-smoke takes no arguments; edit the config block at the top of the script" >&2
  exit 2
fi

if [[ -z "$project" ]]; then
  echo "project is required" >&2
  exit 2
fi
if ! [[ "$timeout_seconds" =~ ^[0-9]+$ ]] || [[ "$timeout_seconds" -lt 1 ]]; then
  echo "timeout must be a positive integer: $timeout_seconds" >&2
  exit 2
fi
if ! [[ "$clip_seconds" =~ ^[0-9]+$ ]] || [[ "$clip_seconds" -lt 6 ]]; then
  echo "clip-seconds must be an integer >= 6: $clip_seconds" >&2
  exit 2
fi
if [[ -n "$host_port" ]] && { ! [[ "$host_port" =~ ^[0-9]+$ ]] || [[ "$host_port" -lt 1 ]] || [[ "$host_port" -gt 65535 ]]; }; then
  echo "host-port must be an integer from 1 to 65535: $host_port" >&2
  exit 2
fi
if ! command -v docker >/dev/null 2>&1; then
  echo "docker is required" >&2
  exit 1
fi
if ! command -v ffmpeg >/dev/null 2>&1; then
  echo "ffmpeg is required to generate and validate smoke media" >&2
  exit 1
fi
if ! command -v jq >/dev/null 2>&1; then
  echo "jq is required to validate schedule and manifest alignment" >&2
  exit 1
fi

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$repo_root"

export COMPOSE_PROJECT_NAME="$project"
network="${project}_default"

tmpdir="$(mktemp -d)"
job_cid=""
ports_override="$tmpdir/docker-compose.ports.yml"
env_override="$tmpdir/docker-compose.env.yml"
cookie_jar="$tmpdir/admin-cookie.jar"
manifest_file="$tmpdir/live.m3u8"
published_host_port=""
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
  cat > "$ports_override" <<'YAML'
services:
  linearcast:
    ports:
YAML
  printf '      - "%s"\n' "$port_spec" >> "$ports_override"
  compose_files+=(-f "$ports_override")
fi

compose() { docker compose "${compose_files[@]}" "$@"; }

cleanup() {
  local status="$?"
  if [[ "$status" -ne 0 ]]; then
    failed=true
  fi
  if [[ "$failed" == true && "$keep_on_failure" == true ]]; then
    echo "keeping failed smoke stack for debugging:"
    echo "  project: $project"
    echo "  temp dir: $tmpdir"
    if [[ -n "${web_base_url:-}" ]]; then
      echo "  url: $web_base_url"
    fi
    if [[ -n "${published_host_port:-}" ]]; then
      echo "  host bind: ${bind_address}:${published_host_port}"
    fi
    echo "  compose: COMPOSE_PROJECT_NAME=$project docker compose ${compose_files[*]} ps"
    return
  fi
  docker network disconnect "$network" "${job_cid:-}" 2>/dev/null || true
  compose down --volumes 2>/dev/null || true
  rm -rf "$tmpdir"
}
trap cleanup EXIT

mkdir -p "$tmpdir/data" "$tmpdir/cache" "$tmpdir/media/phase-a"

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

generate_clip() {
  local label="$1"
  local freq="$2"
  local out="$3"
  ffmpeg -hide_banner -v error -y \
    -f lavfi -i "testsrc2=size=640x360:rate=30:duration=${clip_seconds}" \
    -f lavfi -i "sine=frequency=${freq}:sample_rate=48000:duration=${clip_seconds}" \
    -vf "drawtext=text='${label} %{pts\\:hms}':x=32:y=32:fontsize=36:fontcolor=white:box=1:boxcolor=black@0.75" \
    -c:v libx264 -preset veryfast -pix_fmt yuv420p \
    -c:a aac -b:a 128k -shortest \
    "$out"
}

echo "generating synthetic smoke media..."
clip1="$tmpdir/media/phase-a/linearcast-phase-a-01.mp4"
clip2="$tmpdir/media/phase-a/linearcast-phase-a-02.mp4"
generate_clip "LCA01" 440 "$clip1"
generate_clip "LCA02" 660 "$clip2"
media_ids=("linearcast-phase-a-01" "linearcast-phase-a-02")

compose up -d

if [[ -n "$job_cid" ]]; then
  docker network connect "$network" "$job_cid"
  web_base_url="http://linearcast:8080"
else
  web_port="$(compose port linearcast 8080 | awk -F: 'END {print $NF}')"
  if [[ -z "$web_port" ]]; then
    echo "failed to resolve live smoke localhost web port" >&2
    exit 1
  fi
  published_host_port="$web_port"
  web_base_url="http://127.0.0.1:$web_port"
fi
admin_api_url="$web_base_url"
if [[ -n "$published_host_port" ]]; then
  echo "ok: stack reachable at $web_base_url (host bind ${bind_address}:${published_host_port})"
else
  echo "ok: stack reachable at $web_base_url"
fi

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

dump_debug_state() {
  local channel_id="${1:-}"
  local manifest_url="${2:-}"
  echo "--- live smoke debug ---" >&2
  echo "project=$project" >&2
  echo "temp_dir=$tmpdir" >&2
  echo "web_base_url=${web_base_url:-}" >&2
  if [[ -n "$manifest_url" ]]; then
    echo "manifest_url=$manifest_url" >&2
  fi
  if [[ -n "$channel_id" ]]; then
    echo "--- admin channel now ---" >&2
    curl -fsS -b "$cookie_jar" "$admin_api_url/api/channels/$channel_id/now" >&2 || true
    echo "" >&2
    echo "--- admin channel schedule ---" >&2
    curl -fsS -b "$cookie_jar" "$admin_api_url/api/channels/$channel_id/schedule?horizonHours=1" >&2 || true
    echo "" >&2
  fi
  echo "--- playback status ---" >&2
  curl -fsS "$web_base_url/status" >&2 || true
  echo "" >&2
  echo "--- compose ps ---" >&2
  compose ps >&2 || true
  echo "--- recent logs ---" >&2
  compose logs --tail=160 >&2 || true
}

extract_json_string() {
  local key="$1"
  sed -nE "s/.*\"${key}\"[[:space:]]*:[[:space:]]*\"([^\"]*)\".*/\\1/p" | head -1
}

post_json() {
  local url="$1"
  local body="$2"
  curl -fsS -b "$cookie_jar" -c "$cookie_jar" \
    -H "Content-Type: application/json" \
    -d "$body" \
    "$url"
}

resolve_playlist_url() {
  local base="$1"
  local ref="$2"
  if [[ "$ref" =~ ^https?:// ]]; then
    echo "$ref"
  elif [[ "$ref" == /* ]]; then
    echo "$web_base_url$ref"
  else
    echo "${base%/*}/$ref"
  fi
}

fetch_media_playlist() {
  local url="$1"
  local out="$2"
  local variant_ref=""
  local variant_url=""

  curl -fsS "$url" -o "$out" || return 1
  if grep -q '^#EXTINF:' "$out"; then
    echo "$url"
    return 0
  fi

  variant_ref="$(awk 'prev && $0 !~ /^#/ { print; exit } /^#EXT-X-STREAM-INF:/ { prev=1 }' "$out")"
  if [[ -z "$variant_ref" ]]; then
    return 1
  fi

  variant_url="$(resolve_playlist_url "$url" "$variant_ref")"
  curl -fsS "$variant_url" -o "$out" || return 1
  if grep -q '^#EXTINF:' "$out"; then
    echo "$variant_url"
    return 0
  fi
  return 1
}

current_ms() {
  local seconds nanos
  seconds="$(date -u +%s)"
  nanos="$(date -u +%N)"
  echo $((seconds * 1000 + 10#$nanos / 1000000))
}

package_id_for_media() {
  local media_id="$1"
  jq -r --arg media_id "$media_id" '.media[] | select(.mediaId == $media_id) | .packageId // empty' <<<"$ready_resp" | head -1
}

first_manifest_package_id() {
  sed -nE 's#.*/segments/([^/]+)/[0-9]+\.m4s.*#\1#p' "$manifest_file" | head -1
}

first_manifest_segment_index() {
  sed -nE 's#.*/segments/[^/]+/([0-9]+)\.m4s.*#\1#p' "$manifest_file" | head -1
}

entry_at_ms() {
  local at_ms="$1"
  jq -c --argjson at_ms "$at_ms" '.entries[] | select(.startMs <= $at_ms and $at_ms < .endMs) | {mediaId, startMs, endMs, durationMs, offsetMs: (.offsetMs // 0)}' <<<"$schedule_resp" | head -1
}

next_boundary_pair() {
  local now_ms="$1"
  jq -c --argjson now_ms "$now_ms" '
    .entries as $entries
    | first(range(0; ($entries | length) - 1) as $i
      | select($entries[$i].startMs <= $now_ms and $now_ms < $entries[$i].endMs)
      | {
          current: $entries[$i],
          next: $entries[$i + 1],
          boundaryMs: $entries[$i].endMs
        })
  ' <<<"$schedule_resp"
}

assert_manifest_starts_with_package() {
  local expected_package_id="$1"
  local label="$2"
  local observed_package_id=""

  if ! media_manifest_url="$(fetch_media_playlist "$manifest_url" "$manifest_file" 2>/dev/null)"; then
    echo "failed: could not fetch media playlist for $label" >&2
    dump_debug_state "$channel_id" "$manifest_url"
    exit 1
  fi
  observed_package_id="$(first_manifest_package_id)"
  if [[ -z "$observed_package_id" ]]; then
    echo "failed: $label manifest did not include packaged segment URLs" >&2
    cat "$manifest_file" >&2
    dump_debug_state "$channel_id" "$manifest_url"
    exit 1
  fi
  if [[ "$observed_package_id" != "$expected_package_id" ]]; then
    echo "failed: $label manifest starts with package $observed_package_id, want $expected_package_id" >&2
    cat "$manifest_file" >&2
    dump_debug_state "$channel_id" "$manifest_url"
    exit 1
  fi
  echo "ok: $label manifest starts with expected package ($expected_package_id)"
}

# Assert the media playlist starts at the segment for the current wall-clock
# offset into the scheduled program, not just the right package. The first
# segment URL (/segments/{pkg}/{N}.m4s) carries the media-relative segment index
# N; for a grid-aligned package that index is the offset into the program. This
# is the deterministic, machine-readable offset signal (no frame decode/OCR).
assert_manifest_offset() {
  local label="$1"
  local now_ms entry start_ms offset_ms pos_ms expected observed delta abs

  now_ms="$(current_ms)"
  if ! media_manifest_url="$(fetch_media_playlist "$manifest_url" "$manifest_file" 2>/dev/null)"; then
    echo "failed: could not fetch media playlist for $label offset check" >&2
    dump_debug_state "$channel_id" "$manifest_url"
    exit 1
  fi
  entry="$(entry_at_ms "$now_ms")"
  if [[ -z "$entry" ]]; then
    echo "failed: no schedule entry covers $now_ms for $label offset check" >&2
    dump_debug_state "$channel_id" "$manifest_url"
    exit 1
  fi
  start_ms="$(jq -r '.startMs' <<<"$entry")"
  offset_ms="$(jq -r '.offsetMs' <<<"$entry")"
  observed="$(first_manifest_segment_index)"
  if [[ -z "$observed" ]]; then
    echo "failed: $label manifest did not include a packaged segment index" >&2
    cat "$manifest_file" >&2
    dump_debug_state "$channel_id" "$manifest_url"
    exit 1
  fi
  pos_ms=$((offset_ms + (now_ms - start_ms)))
  if [[ "$pos_ms" -lt 0 ]]; then
    pos_ms=0
  fi
  expected=$((pos_ms / segment_ms))
  delta=$((observed - expected))
  abs=${delta#-}
  if [[ "$abs" -gt "$offset_tolerance_segments" ]]; then
    echo "failed: $label first segment index $observed, expected ~$expected (offset ${pos_ms}ms into program, tol=${offset_tolerance_segments})" >&2
    cat "$manifest_file" >&2
    dump_debug_state "$channel_id" "$manifest_url"
    exit 1
  fi
  echo "ok: $label first segment index $observed ~ expected $expected (offset ${pos_ms}ms into program)"
}

wait_for_ok "$web_base_url/healthz" "web healthz"
wait_for_ok "$admin_api_url/api/healthz" "admin healthz"

echo "starting ingest..."
ingest_resp="$(post_json "$admin_api_url/api/ingest" "{\"path\":\"$tmpdir/media/phase-a\"}")"
ingest_id="$(extract_json_string "jobId" <<<"$ingest_resp")"
if [[ -z "$ingest_id" ]]; then
  echo "failed: ingest response did not include jobId: $ingest_resp" >&2
  exit 1
fi

for _ in $(seq 1 "$timeout_seconds"); do
  ingest_status="$(curl -fsS -b "$cookie_jar" "$admin_api_url/api/ingest/$ingest_id")"
  status="$(extract_json_string "status" <<<"$ingest_status")"
  case "$status" in
    done)
      if ! grep -q '"passed"[[:space:]]*:[[:space:]]*2' <<<"$ingest_status"; then
        echo "failed: ingest completed without two passed files: $ingest_status" >&2
        exit 1
      fi
      echo "ok: ingest complete"
      break
      ;;
    failed|cancelled)
      echo "failed: ingest status=$status: $ingest_status" >&2
      exit 1
      ;;
  esac
  sleep 1
done
if [[ "${status:-}" != "done" ]]; then
  echo "failed: ingest did not complete within ${timeout_seconds}s" >&2
  exit 1
fi

media_ids_json="[\"${media_ids[0]}\",\"${media_ids[1]}\"]"
channel_name="Linearcast Phase A Smoke $(date -u +%Y%m%d%H%M%S)"
create_body="{\"displayName\":\"$channel_name\",\"packageProfile\":\"$profile\",\"mediaIds\":$media_ids_json,\"ordering\":\"block\",\"scheduleMode\":\"back_to_back\",\"prefillMode\":\"eager\"}"
echo "creating packaged smoke channel..."
create_resp="$(post_json "$admin_api_url/api/schedule-builder/channels" "$create_body")"
channel_id="$(extract_json_string "channelID" <<<"$create_resp")"
if [[ -z "$channel_id" ]]; then
  echo "failed: create channel response did not include channelID: $create_resp" >&2
  exit 1
fi
echo "ok: channel created ($channel_id)"

count_ids_in_response() {
  local response="$1"
  local found=0
  local mid
  for mid in "${media_ids[@]}"; do
    grep -qF "\"$mid\"" <<<"$response" && found=$((found + 1)) || true
  done
  echo "$found"
}

echo "waiting for packages..."
ready=0
for _ in $(seq 1 "$timeout_seconds"); do
  failed_resp="$(curl -fsS -b "$cookie_jar" "$admin_api_url/api/media/package-candidates?profile=$profile&status=failed&limit=100" 2>/dev/null || true)"
  if [[ "$(count_ids_in_response "$failed_resp")" -gt 0 ]]; then
    echo "failed: at least one smoke package failed: $failed_resp" >&2
    exit 1
  fi

  ready_resp="$(curl -fsS -b "$cookie_jar" "$admin_api_url/api/media/package-candidates?profile=$profile&status=ready&limit=100" 2>/dev/null || true)"
  ready="$(count_ids_in_response "$ready_resp")"
  if [[ "$ready" -eq "${#media_ids[@]}" ]]; then
    echo "ok: packages ready"
    break
  fi
  sleep 1
done
if [[ "$ready" -ne "${#media_ids[@]}" ]]; then
  echo "failed: packages did not become ready within ${timeout_seconds}s ($ready/${#media_ids[@]} ready)" >&2
  exit 1
fi

echo "extending schedule with ready packages..."
extend_resp="$(post_json "$admin_api_url/api/channels/$channel_id/extend" "{\"hours\":1}")"
if ! grep -q '"inserted"[[:space:]]*:[[:space:]]*[1-9]' <<<"$extend_resp"; then
  echo "failed: schedule extend did not insert entries: $extend_resp" >&2
  dump_debug_state "$channel_id" ""
  exit 1
fi
echo "ok: schedule extended"

echo "waiting for schedule entries..."
schedule_ready=false
for _ in $(seq 1 30); do
  schedule_resp="$(curl -fsS -b "$cookie_jar" "$admin_api_url/api/channels/$channel_id/schedule?horizonHours=1" 2>/dev/null || true)"
  if grep -q '"entries"[[:space:]]*:[[:space:]]*\[' <<<"$schedule_resp" && grep -q '"mediaId"' <<<"$schedule_resp"; then
    schedule_ready=true
    echo "ok: schedule entries present"
    break
  fi
  sleep 1
done
if [[ "$schedule_ready" != true ]]; then
  echo "failed: schedule entries did not appear after extend" >&2
  dump_debug_state "$channel_id" ""
  exit 1
fi

echo "waiting for playable manifest..."
manifest_url="$web_base_url/channels/$channel_id/stream.m3u8"
media_manifest_url=""
for _ in $(seq 1 90); do
  if media_manifest_url="$(fetch_media_playlist "$manifest_url" "$manifest_file" 2>/dev/null)"; then
    echo "ok: manifest contains segments"
    break
  fi
  sleep 2
done
if ! grep -q '^#EXTINF:' "$manifest_file" 2>/dev/null; then
  echo "failed: manifest did not contain segments" >&2
  curl -fsS "$manifest_url" || true
  dump_debug_state "$channel_id" "$manifest_url"
  exit 1
fi

echo "validating schedule-to-manifest alignment..."
schedule_resp="$(curl -fsS -b "$cookie_jar" "$admin_api_url/api/channels/$channel_id/schedule?horizonHours=1")"
check_ms="$(current_ms)"
current_entry="$(entry_at_ms "$check_ms")"
if [[ -z "$current_entry" ]]; then
  echo "failed: no schedule entry covers current time $check_ms" >&2
  dump_debug_state "$channel_id" "$manifest_url"
  exit 1
fi
current_media_id="$(jq -r '.mediaId' <<<"$current_entry")"
current_package_id="$(package_id_for_media "$current_media_id")"
if [[ -z "$current_package_id" ]]; then
  echo "failed: no ready package ID found for current media $current_media_id" >&2
  dump_debug_state "$channel_id" "$manifest_url"
  exit 1
fi
assert_manifest_starts_with_package "$current_package_id" "current-entry"

# Sample mid-program so the offset assertion exercises a non-zero segment index
# (proves wall-clock offset positioning, not just "starts at segment 0"). The
# post-boundary check below intentionally lands near index 0 to prove the boundary
# resets to program start.
current_start_ms="$(jq -r '.startMs' <<<"$current_entry")"
current_duration_ms="$(jq -r '.durationMs' <<<"$current_entry")"
mid_target_ms=$((current_start_ms + current_duration_ms / 2))
mid_now_ms="$(current_ms)"
if [[ "$mid_target_ms" -gt "$mid_now_ms" ]]; then
  mid_sleep_seconds=$(((mid_target_ms - mid_now_ms + 999) / 1000))
  echo "waiting ${mid_sleep_seconds}s to sample mid-program offset..."
  sleep "$mid_sleep_seconds"
fi
assert_manifest_offset "current-entry"

boundary_pair="$(next_boundary_pair "$check_ms")"
if [[ -z "$boundary_pair" || "$boundary_pair" == "null" ]]; then
  echo "failed: schedule did not include a current entry followed by a boundary" >&2
  dump_debug_state "$channel_id" "$manifest_url"
  exit 1
fi
boundary_ms="$(jq -r '.boundaryMs' <<<"$boundary_pair")"
next_media_id="$(jq -r '.next.mediaId' <<<"$boundary_pair")"
next_package_id="$(package_id_for_media "$next_media_id")"
if [[ -z "$next_package_id" ]]; then
  echo "failed: no ready package ID found for next media $next_media_id" >&2
  dump_debug_state "$channel_id" "$manifest_url"
  exit 1
fi

target_ms=$((boundary_ms + 2000))
now_ms="$(current_ms)"
if [[ "$target_ms" -gt "$now_ms" ]]; then
  sleep_ms=$((target_ms - now_ms))
  sleep_seconds=$(((sleep_ms + 999) / 1000))
  echo "waiting ${sleep_seconds}s for schedule boundary into $next_media_id..."
  sleep "$sleep_seconds"
fi

after_boundary_ms="$(current_ms)"
after_boundary_entry="$(entry_at_ms "$after_boundary_ms")"
if [[ -n "$after_boundary_entry" ]]; then
  after_boundary_media_id="$(jq -r '.mediaId // empty' <<<"$after_boundary_entry")"
else
  after_boundary_media_id=""
fi
if [[ "$after_boundary_media_id" != "$next_media_id" ]]; then
  echo "failed: expected to be in $next_media_id after boundary, got ${after_boundary_media_id:-none}" >&2
  dump_debug_state "$channel_id" "$manifest_url"
  exit 1
fi
assert_manifest_starts_with_package "$next_package_id" "post-boundary"
assert_manifest_offset "post-boundary"

echo "validating served playlist decode..."
ffmpeg -hide_banner -v error -nostdin -t 12 -i "$media_manifest_url" -f null -

echo "Live playback smoke passed: channel=$channel_id url=$manifest_url"
