#!/usr/bin/env bash
set -euo pipefail

# Deterministic generated-media acceptance for the already-built
# linearcast:local image. Keep defaults CI-safe; explicit flags only provide
# isolation and timeout control.
project="linearcast-live-smoke"
timeout_seconds=600
clip_seconds=18
profile="h264-1080p-8mbps"
copy_profile="hevc-copy-source"
segment_ms=6000             # durable package cadence; on-demand encodings use 2000ms
offset_tolerance_segments=1 # allow +/-1 segment of wall-clock vs manifest skew
keep_on_failure=false
host_port=""
bind_address="127.0.0.1"

failed=false

usage() {
  cat <<'EOF'
Usage: scripts/smoke/live-playback-smoke.sh [options]

Options:
  --project <name>       Unique Compose project name.
  --timeout <seconds>    Per-phase wait timeout (default: 600).
  --keep-on-failure      Keep the failed stack and fixture directory for debugging.
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --project) project="${2:?missing value for --project}"; shift 2 ;;
    --timeout) timeout_seconds="${2:?missing value for --timeout}"; shift 2 ;;
    --keep-on-failure) keep_on_failure=true; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "Unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done

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
if ! command -v node >/dev/null 2>&1; then
  echo "node is required to validate API JSON" >&2
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
master_file="$tmpdir/master.m3u8"
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
  if [[ "$failed" == true ]]; then
    echo "--- live playback smoke stack logs ---" >&2
    compose logs --tail=240 >&2 || true
  fi
  docker network disconnect "$network" "${job_cid:-}" 2>/dev/null || true
  compose down --volumes 2>/dev/null || true
  rm -rf "$tmpdir"
}
trap cleanup EXIT

mkdir -p "$tmpdir/data" "$tmpdir/cache" "$tmpdir/media/acceptance"

export LINEARCAST_DATA_DIR="$tmpdir/data"
export LINEARCAST_CACHE_DIR="$tmpdir/cache"
export LINEARCAST_MEDIA_ROOT="$tmpdir/media"
export LINEARCAST_DB="$tmpdir/data/linearcast.db"
export CACHE_DIR="$tmpdir/cache"
export LINEARCAST_ADDR=":8888"
export LINEARCAST_ADMIN_ALLOW_NO_AUTH="true"
export LINEARCAST_ON_DEMAND_PLAYBACK_LAG_MS="6000"
export LINEARCAST_ON_DEMAND_WARMUP_MS="4000"
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
      LINEARCAST_ADMIN_ALLOW_NO_AUTH: "${LINEARCAST_ADMIN_ALLOW_NO_AUTH}"
      LINEARCAST_ON_DEMAND_PLAYBACK_LAG_MS: "${LINEARCAST_ON_DEMAND_PLAYBACK_LAG_MS}"
      LINEARCAST_ON_DEMAND_WARMUP_MS: "${LINEARCAST_ON_DEMAND_WARMUP_MS}"
      TZ: "${TZ}"
YAML
compose_files+=(-f "$env_override")

generate_clip() {
  local freq="$1"
  local out="$2"
  docker run --rm --user "$(id -u):$(id -g)" \
    -v "$tmpdir/media:/media" \
    --entrypoint ffmpeg linearcast:local \
    -hide_banner -v error -y \
    -f lavfi -i "testsrc2=size=640x360:rate=30:duration=${clip_seconds}" \
    -f lavfi -i "sine=frequency=${freq}:sample_rate=48000:duration=${clip_seconds}" \
    -c:v libx264 -preset veryfast -pix_fmt yuv420p \
    -g 60 -keyint_min 60 -sc_threshold 0 \
    -c:a aac -b:a 128k -shortest \
    "$out"
}

generate_subtitle_clip() {
  local out="$1"
  docker run --rm --user "$(id -u):$(id -g)" \
    -v "$tmpdir/media:/media" \
    --entrypoint ffmpeg linearcast:local \
    -hide_banner -v error -y \
    -f lavfi -i "testsrc2=size=640x360:rate=30:duration=${clip_seconds}" \
    -f lavfi -i "sine=frequency=330:sample_rate=48000:duration=${clip_seconds}" \
    -f srt -i /media/acceptance/fixture-subtitle.srt \
    -map 0:v:0 -map 1:a:0 -map 2:s:0 \
    -c:v libx264 -preset veryfast -pix_fmt yuv420p \
    -g 60 -keyint_min 60 -sc_threshold 0 \
    -c:a aac -b:a 128k -c:s mov_text \
    -metadata:s:s:0 language=eng -t "$clip_seconds" \
    "$out"
}

generate_hevc_copy_clip() {
  local out="$1"
  docker run --rm --user "$(id -u):$(id -g)" \
    -v "$tmpdir/media:/media" \
    --entrypoint ffmpeg linearcast:local \
    -hide_banner -v error -y \
    -f lavfi -i "testsrc2=size=320x180:rate=30:duration=${clip_seconds}" \
    -f lavfi -i "sine=frequency=880:sample_rate=48000:duration=${clip_seconds}" \
    -vf format=yuv420p10le \
    -c:v libx265 -preset ultrafast -pix_fmt yuv420p10le -tag:v hvc1 \
    -x265-params "log-level=error:pools=none:frame-threads=1:repeat-headers=1:keyint=60:min-keyint=60:scenecut=0:colorprim=bt2020:transfer=smpte2084:colormatrix=bt2020nc" \
    -color_primaries bt2020 -color_trc smpte2084 -colorspace bt2020nc \
    -c:a aac -b:a 128k -shortest \
    "$out"
}

if ! docker image inspect linearcast:local >/dev/null 2>&1; then
  echo "linearcast:local is required; run docker compose build first" >&2
  exit 1
fi

cat > "$tmpdir/media/acceptance/fixture-subtitle.srt" <<EOF
1
00:00:00,000 --> 00:00:${clip_seconds},000
LC_SUBTITLE deterministic WebVTT acceptance
EOF

echo "generating deterministic acceptance media..."
generate_subtitle_clip /media/acceptance/fixture-subtitle-webvtt.mp4
generate_clip 440 /media/acceptance/fixture-h264-boundary-a.mp4
generate_clip 660 /media/acceptance/fixture-h264-boundary-b.mp4
generate_clip 550 /media/acceptance/fixture-ondemand-a.mp4
generate_clip 770 /media/acceptance/fixture-ondemand-b.mp4
generate_hevc_copy_clip /media/acceptance/fixture-hevc-copy.mp4

eager_media_ids=("fixture-subtitle-webvtt" "fixture-h264-boundary-a" "fixture-h264-boundary-b")
on_demand_media_ids=("fixture-ondemand-a" "fixture-ondemand-b")
copy_media_ids=("fixture-hevc-copy")
all_media_ids=("${eager_media_ids[@]}" "${on_demand_media_ids[@]}" "${copy_media_ids[@]}")

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
    (curl -fsS -b "$cookie_jar" "$admin_api_url/api/channels/$channel_id/schedule?hours=1" \
      | node -e '
          const fs = require("fs");
          const body = JSON.parse(fs.readFileSync(0, "utf8"));
          body.entries = (body.entries ?? []).slice(0, 8);
          process.stdout.write(JSON.stringify(body));
        ') >&2 || true
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

json_field() {
  local path="$1"
  node -e '
    const fs = require("fs");
    let value = JSON.parse(fs.readFileSync(0, "utf8"));
    for (const key of process.argv[1].split(".")) value = value?.[key];
    if (value !== undefined && value !== null) process.stdout.write(String(value));
  ' "$path"
}

json_array() {
  node -e 'process.stdout.write(JSON.stringify(process.argv.slice(1)))' "$@"
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
  node -e '
    const fs = require("fs");
    const body = JSON.parse(fs.readFileSync(0, "utf8"));
    const row = body.media?.find((item) => item.mediaId === process.argv[1]);
    if (row?.packageId) process.stdout.write(row.packageId);
  ' "$media_id" <<<"$ready_resp"
}

first_manifest_package_id() {
  sed -nE 's#^(.*/)?segments/([^/]+)/[0-9]+\.m4s.*#\2#p' "$manifest_file" | head -1
}

first_manifest_segment_index() {
  sed -nE 's#^(.*/)?segments/[^/]+/([0-9]+)\.m4s.*#\2#p' "$manifest_file" | head -1
}

entry_at_ms() {
  local at_ms="$1"
  node -e '
    const fs = require("fs");
    const body = JSON.parse(fs.readFileSync(0, "utf8"));
    const at = Number(process.argv[1]);
    const row = body.entries?.find((entry) => entry.startMs <= at && at < entry.endMs);
    if (row) process.stdout.write(JSON.stringify({
      entryId: row.entryId,
      mediaId: row.mediaId,
      startMs: row.startMs,
      endMs: row.endMs,
      durationMs: row.durationMs,
      offsetMs: row.offsetMs ?? 0,
    }));
  ' "$at_ms" <<<"$schedule_resp"
}

next_boundary_pair() {
  local now_ms="$1"
  node -e '
    const fs = require("fs");
    const body = JSON.parse(fs.readFileSync(0, "utf8"));
    const now = Number(process.argv[1]);
    const entries = body.entries ?? [];
    const index = entries.findIndex((entry) => entry.startMs <= now && now < entry.endMs);
    if (index >= 0 && index + 1 < entries.length) {
      process.stdout.write(JSON.stringify({
        current: entries[index],
        next: entries[index + 1],
        boundaryMs: entries[index].endMs,
      }));
    }
  ' "$now_ms" <<<"$schedule_resp"
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
  start_ms="$(json_field startMs <<<"$entry")"
  offset_ms="$(json_field offsetMs <<<"$entry")"
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

container_url() {
  local url="$1"
  echo "http://linearcast:8080${url#"$web_base_url"}"
}

first_manifest_init_uri() {
  sed -nE 's/^#EXT-X-MAP:URI="([^"]+)".*/\1/p' "$manifest_file" | head -1
}

validate_manifest_artifacts() {
  local label="$1"
  local root_manifest_url="$2"
  local expected_codec="$3"
  local subtitle_marker="${4:-}"
  local variant_ref variant_url init_ref init_url segment_ref segment_url
  local subtitle_ref subtitle_url subtitle_segment_ref subtitle_segment_url codec

  curl -fsS "$root_manifest_url" -o "$master_file"
  if ! grep -q '^#EXT-X-STREAM-INF:' "$master_file"; then
    echo "failed: $label master manifest has no rendition" >&2
    cat "$master_file" >&2
    exit 1
  fi
  variant_ref="$(awk 'prev && $0 !~ /^#/ { print; exit } /^#EXT-X-STREAM-INF:/ { prev=1 }' "$master_file")"
  variant_url="$(resolve_playlist_url "$root_manifest_url" "$variant_ref")"
  curl -fsS "$variant_url" -o "$manifest_file"

  init_ref="$(first_manifest_init_uri)"
  segment_ref="$(awk '$0 !~ /^#/ && NF { print; exit }' "$manifest_file")"
  if [[ -z "$init_ref" || -z "$segment_ref" ]]; then
    echo "failed: $label rendition is missing init or media artifacts" >&2
    cat "$manifest_file" >&2
    exit 1
  fi
  init_url="$(resolve_playlist_url "$variant_url" "$init_ref")"
  segment_url="$(resolve_playlist_url "$variant_url" "$segment_ref")"
  curl -fsS "$init_url" -o "$tmpdir/${label}-init.mp4"
  curl -fsS "$segment_url" -o "$tmpdir/${label}-segment.m4s"
  if [[ ! -s "$tmpdir/${label}-init.mp4" || ! -s "$tmpdir/${label}-segment.m4s" ]]; then
    echo "failed: $label init or media artifact is empty" >&2
    exit 1
  fi

  codec="$(docker run --rm --network "$network" --entrypoint ffprobe linearcast:local \
    -v error -select_streams v:0 -show_entries stream=codec_name \
    -of default=noprint_wrappers=1:nokey=1 "$(container_url "$variant_url")" | head -1)"
  if [[ "$codec" != "$expected_codec" ]]; then
    echo "failed: $label video codec=$codec, want $expected_codec" >&2
    exit 1
  fi

  if [[ -n "$subtitle_marker" ]]; then
    subtitle_ref="$(sed -nE 's/^#EXT-X-MEDIA:TYPE=SUBTITLES.*URI="([^"]+)".*/\1/p' "$master_file" | head -1)"
    if [[ -z "$subtitle_ref" ]] || ! grep -q 'SUBTITLES="subs"' "$master_file"; then
      echo "failed: $label master does not advertise its WebVTT rendition" >&2
      cat "$master_file" >&2
      exit 1
    fi
    subtitle_url="$(resolve_playlist_url "$root_manifest_url" "$subtitle_ref")"
    curl -fsS "$subtitle_url" -o "$tmpdir/${label}-subtitle.m3u8"
    subtitle_segment_ref="$(awk '$0 !~ /^#/ && $0 !~ /empty\.vtt/ && NF { print; exit }' "$tmpdir/${label}-subtitle.m3u8")"
    if [[ -z "$subtitle_segment_ref" ]]; then
      echo "failed: $label subtitle playlist contains no WebVTT segment" >&2
      cat "$tmpdir/${label}-subtitle.m3u8" >&2
      exit 1
    fi
    subtitle_segment_url="$(resolve_playlist_url "$subtitle_url" "$subtitle_segment_ref")"
    curl -fsS "$subtitle_segment_url" -o "$tmpdir/${label}.vtt"
    if ! head -1 "$tmpdir/${label}.vtt" | grep -q '^WEBVTT' || ! grep -qF "$subtitle_marker" "$tmpdir/${label}.vtt"; then
      echo "failed: $label WebVTT segment is missing the deterministic cue" >&2
      cat "$tmpdir/${label}.vtt" >&2
      exit 1
    fi
  fi

  docker run --rm --network "$network" --entrypoint ffmpeg linearcast:local \
    -hide_banner -v error -nostdin -t 6 -i "$(container_url "$variant_url")" -f null -
  validated_media_url="$variant_url"
  echo "ok: $label artifact, codec, and decode checks passed"
}

assert_time_surfaces_agree() {
  local label="$1"
  local observed_ms="$2"
  local from_ms=$((observed_ms - clip_seconds * 1000))
  local now_resp guide_resp
  now_resp="$(curl -fsS "$admin_api_url/api/channels/$channel_id/now")"
  guide_resp="$(curl -fsS "$admin_api_url/api/guide?from=$from_ms&hours=1")"
  node -e '
    const [label, channelId, observedRaw, scheduleRaw, nowRaw, guideRaw] = process.argv.slice(1);
    const observed = Number(observedRaw);
    const schedule = JSON.parse(scheduleRaw);
    const now = JSON.parse(nowRaw);
    const guide = JSON.parse(guideRaw);
    const scheduled = schedule.entries?.find((entry) => entry.startMs <= observed && observed < entry.endMs);
    const guideChannel = guide.channels?.find((channel) => channel.id === channelId);
    const guided = guideChannel?.entries?.find((entry) => entry.startMs <= observed && observed < entry.endMs);
    const fail = (message) => { console.error(`failed: ${label} ${message}`); process.exit(1); };
    if (!scheduled) fail("schedule has no current entry");
    if (!now.current) fail("now endpoint has no current entry");
    if (!guided) fail("guide has no current entry");
    if (now.current.mediaID !== scheduled.mediaId || now.current.startMs !== scheduled.startMs || now.current.endMs !== scheduled.endMs) {
      fail("now and schedule disagree");
    }
    if (guided.mediaId !== scheduled.mediaId || guided.startMs !== scheduled.startMs || guided.endMs !== scheduled.endMs) {
      fail("guide and schedule disagree");
    }
    if (Math.abs(guide.nowMs - observed) > 5000) fail("guide clock drifted outside the observation window");
  ' "$label" "$channel_id" "$observed_ms" "$schedule_resp" "$now_resp" "$guide_resp"
  echo "ok: $label schedule, now, and guide agree at observed wall clock $observed_ms"
}

wait_for_ok "$web_base_url/healthz" "web healthz"
wait_for_ok "$admin_api_url/api/healthz" "admin healthz"

echo "starting ingest..."
ingest_resp="$(post_json "$admin_api_url/api/ingest" "{\"path\":\"$tmpdir/media/acceptance\"}")"
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
      if ! grep -q "\"passed\"[[:space:]]*:[[:space:]]*${#all_media_ids[@]}" <<<"$ingest_status"; then
        echo "failed: ingest completed without ${#all_media_ids[@]} passed files: $ingest_status" >&2
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

media_ids_json="$(json_array "${eager_media_ids[@]}")"
channel_name="Linearcast HLS Acceptance $(date -u +%Y%m%d%H%M%S)"
create_body="{\"displayName\":\"$channel_name\",\"packageProfile\":\"$profile\",\"mediaIds\":$media_ids_json,\"ordering\":\"block\",\"scheduleMode\":\"back_to_back\",\"prefillMode\":\"eager\"}"
echo "creating packaged smoke channel..."
create_resp="$(post_json "$admin_api_url/api/channels" "$create_body")"
channel_id="$(extract_json_string "channelID" <<<"$create_resp")"
if [[ -z "$channel_id" ]]; then
  echo "failed: create channel response did not include channelID: $create_resp" >&2
  exit 1
fi
echo "ok: channel created ($channel_id)"

count_ids_in_response() {
  local response="$1"
  shift
  local found=0
  local mid
  for mid in "$@"; do
    grep -qF "\"$mid\"" <<<"$response" && found=$((found + 1)) || true
  done
  echo "$found"
}

wait_for_packages() {
  local target_profile="$1"
  shift
  local ids=("$@")
  local ready=0
  local failed_resp=""
  echo "waiting for ${target_profile} packages (${ids[*]})..."
  for _ in $(seq 1 "$timeout_seconds"); do
    failed_resp="$(curl -fsS -b "$cookie_jar" "$admin_api_url/api/media/package-candidates?profile=$target_profile&status=failed&limit=100" 2>/dev/null || true)"
    if [[ "$(count_ids_in_response "$failed_resp" "${ids[@]}")" -gt 0 ]]; then
      echo "failed: at least one $target_profile package failed: $failed_resp" >&2
      exit 1
    fi

    ready_resp="$(curl -fsS -b "$cookie_jar" "$admin_api_url/api/media/package-candidates?profile=$target_profile&status=ready&limit=100" 2>/dev/null || true)"
    ready="$(count_ids_in_response "$ready_resp" "${ids[@]}")"
    if [[ "$ready" -eq "${#ids[@]}" ]]; then
      echo "ok: $target_profile packages ready"
      return 0
    fi
    sleep 1
  done
  echo "failed: $target_profile packages did not become ready within ${timeout_seconds}s ($ready/${#ids[@]} ready)" >&2
  exit 1
}

wait_for_packages "$profile" "${eager_media_ids[@]}"

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
  schedule_resp="$(curl -fsS -b "$cookie_jar" "$admin_api_url/api/channels/$channel_id/schedule?hours=1" 2>/dev/null || true)"
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
schedule_resp="$(curl -fsS -b "$cookie_jar" "$admin_api_url/api/channels/$channel_id/schedule?hours=1")"
check_ms="$(current_ms)"
current_entry="$(entry_at_ms "$check_ms")"
if [[ -z "$current_entry" ]]; then
  echo "failed: no schedule entry covers current time $check_ms" >&2
  dump_debug_state "$channel_id" "$manifest_url"
  exit 1
fi
assert_time_surfaces_agree "eager" "$check_ms"
validate_manifest_artifacts "eager" "$manifest_url" "h264" "LC_SUBTITLE"
current_media_id="$(json_field mediaId <<<"$current_entry")"
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
current_start_ms="$(json_field startMs <<<"$current_entry")"
current_duration_ms="$(json_field durationMs <<<"$current_entry")"
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
boundary_ms="$(json_field boundaryMs <<<"$boundary_pair")"
next_media_id="$(json_field next.mediaId <<<"$boundary_pair")"
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
  after_boundary_media_id="$(json_field mediaId <<<"$after_boundary_entry")"
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
docker run --rm --network "$network" --entrypoint ffmpeg linearcast:local \
  -hide_banner -v error -nostdin -t 6 -i "$(container_url "$media_manifest_url")" -f null -

echo "Eager HLS acceptance passed: channel=$channel_id url=$manifest_url"

echo "creating HEVC copy/remux acceptance channel..."
copy_body="{\"displayName\":\"Linearcast HEVC Copy Acceptance\",\"packageProfile\":\"$copy_profile\",\"mediaIds\":$(json_array "${copy_media_ids[@]}"),\"ordering\":\"block\",\"scheduleMode\":\"back_to_back\",\"prefillMode\":\"eager\"}"
copy_resp="$(post_json "$admin_api_url/api/channels" "$copy_body")"
channel_id="$(extract_json_string "channelID" <<<"$copy_resp")"
if [[ -z "$channel_id" ]]; then
  echo "failed: copy channel response did not include channelID: $copy_resp" >&2
  exit 1
fi
wait_for_packages "$copy_profile" "${copy_media_ids[@]}"
extend_resp="$(post_json "$admin_api_url/api/channels/$channel_id/extend" "{\"hours\":1}")"
if ! grep -q '"inserted"[[:space:]]*:[[:space:]]*[1-9]' <<<"$extend_resp"; then
  echo "failed: copy schedule extend did not insert entries: $extend_resp" >&2
  exit 1
fi
schedule_resp="$(curl -fsS "$admin_api_url/api/channels/$channel_id/schedule?hours=1")"
if ! grep -q '"mediaId"' <<<"$schedule_resp"; then
  echo "failed: copy channel schedule is empty: $schedule_resp" >&2
  exit 1
fi
manifest_url="$web_base_url/channels/$channel_id/stream.m3u8"
for _ in $(seq 1 "$timeout_seconds"); do
  if fetch_media_playlist "$manifest_url" "$manifest_file" >/dev/null 2>&1; then
    break
  fi
  sleep 1
done
if ! grep -q '^#EXTINF:' "$manifest_file" 2>/dev/null; then
  echo "failed: copy channel manifest did not become playable" >&2
  exit 1
fi
validate_manifest_artifacts "copy" "$manifest_url" "hevc"
echo "HEVC copy/remux acceptance passed: channel=$channel_id url=$manifest_url"

echo "creating on-demand boundary acceptance channel..."
on_demand_body="{\"displayName\":\"Linearcast On Demand Acceptance\",\"packageProfile\":\"$profile\",\"mediaIds\":$(json_array "${on_demand_media_ids[@]}"),\"ordering\":\"block\",\"scheduleMode\":\"back_to_back\",\"prefillMode\":\"on_demand\"}"
on_demand_resp="$(post_json "$admin_api_url/api/channels" "$on_demand_body")"
channel_id="$(extract_json_string "channelID" <<<"$on_demand_resp")"
if [[ -z "$channel_id" ]]; then
  echo "failed: on-demand channel response did not include channelID: $on_demand_resp" >&2
  exit 1
fi

schedule_resp="$(curl -fsS "$admin_api_url/api/channels/$channel_id/schedule?hours=1")"
if [[ "$(node -e 'const b=JSON.parse(process.argv[1]); process.stdout.write(String(b.entries?.length ?? 0))' "$schedule_resp")" -lt 2 ]]; then
  echo "failed: on-demand channel did not create a boundary-bearing schedule: $schedule_resp" >&2
  exit 1
fi

missing_resp="$(curl -fsS "$admin_api_url/api/media/package-candidates?profile=$profile&status=missing&limit=100")"
if [[ "$(count_ids_in_response "$missing_resp" "${on_demand_media_ids[@]}")" -ne "${#on_demand_media_ids[@]}" ]]; then
  echo "failed: on-demand fixtures unexpectedly had durable packages before tune: $missing_resp" >&2
  exit 1
fi

manifest_url="$web_base_url/channels/$channel_id/stream.m3u8"
for _ in $(seq 1 "$timeout_seconds"); do
  if fetch_media_playlist "$manifest_url" "$manifest_file" >/dev/null 2>&1; then
    break
  fi
  sleep 1
done
if ! grep -q '^#EXTINF:' "$manifest_file" 2>/dev/null; then
  echo "failed: on-demand channel never crossed its ready gate" >&2
  dump_debug_state "$channel_id" "$manifest_url"
  exit 1
fi
validate_manifest_artifacts "on-demand-before" "$manifest_url" "h264"

metrics_resp="$(curl -fsS "$web_base_url/metrics")"
if ! awk '$1 == "linearcast_on_demand_encoding_spawns_total" && $2 + 0 > 0 { found=1 } END { exit !found }' <<<"$metrics_resp"; then
  echo "failed: on-demand tune did not record an encoding spawn" >&2
  exit 1
fi

schedule_resp="$(curl -fsS "$admin_api_url/api/channels/$channel_id/schedule?hours=1")"
check_ms="$(current_ms)"
assert_time_surfaces_agree "on-demand" "$check_ms"
boundary_pair="$(next_boundary_pair "$check_ms")"
if [[ -z "$boundary_pair" ]]; then
  echo "failed: on-demand schedule has no next boundary" >&2
  exit 1
fi
boundary_ms="$(json_field boundaryMs <<<"$boundary_pair")"
next_media_id="$(json_field next.mediaId <<<"$boundary_pair")"
target_ms=$((boundary_ms + 2000))
now_ms="$(current_ms)"
if [[ "$target_ms" -gt "$now_ms" ]]; then
  sleep_seconds=$(((target_ms - now_ms + 999) / 1000))
  echo "waiting ${sleep_seconds}s for on-demand boundary into $next_media_id..."
  sleep "$sleep_seconds"
fi

boundary_observed=false
for _ in $(seq 1 12); do
  now_resp="$(curl -fsS "$admin_api_url/api/channels/$channel_id/now")"
  if [[ "$(json_field current.mediaID <<<"$now_resp")" == "$next_media_id" ]] \
    && fetch_media_playlist "$manifest_url" "$manifest_file" >/dev/null 2>&1; then
    boundary_observed=true
    break
  fi
  sleep 1
done
if [[ "$boundary_observed" != true ]]; then
  echo "failed: on-demand playback did not enter $next_media_id with a playable manifest: $now_resp" >&2
  dump_debug_state "$channel_id" "$manifest_url"
  exit 1
fi
validate_manifest_artifacts "on-demand-after" "$manifest_url" "h264"

missing_resp="$(curl -fsS "$admin_api_url/api/media/package-candidates?profile=$profile&status=missing&limit=100")"
if [[ "$(count_ids_in_response "$missing_resp" "${on_demand_media_ids[@]}")" -ne "${#on_demand_media_ids[@]}" ]]; then
  echo "failed: on-demand tune mutated durable package state: $missing_resp" >&2
  exit 1
fi

echo "Deterministic real-media HLS acceptance passed"
