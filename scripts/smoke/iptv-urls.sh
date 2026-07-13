#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/smoke/iptv-urls.sh [--base-url <url>] [--public-url <url>]

Checks the IPTV/DVR URL settings and public guide endpoints on a running
linearcast admin/nginx endpoint.

Options:
  --base-url <url>    Admin/nginx base URL to test (default: http://127.0.0.1:${WEB_UI_PORT:-8080})
  --public-url <url>  Public server URL to save and verify
                      (default: http://linearcast-smoke.test:${WEB_UI_PORT:-8080})

Environment:
  BASE_URL            Same as --base-url
  PUBLIC_URL          Same as --public-url
  WEB_UI_PORT         Used by defaults when URLs are omitted (default: 8080)
EOF
}

web_ui_port="${WEB_UI_PORT:-8080}"
base_url="${BASE_URL:-http://127.0.0.1:${web_ui_port}}"
public_url="${PUBLIC_URL:-http://linearcast-smoke.test:${web_ui_port}}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --base-url)
      base_url="${2:-}"
      shift 2
      ;;
    --public-url)
      public_url="${2:-}"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "Unknown argument: $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

base_url="${base_url%/}"
public_url="${public_url%/}"

if [[ -z "$base_url" || -z "$public_url" ]]; then
  usage >&2
  exit 2
fi

echo "saving public server URL: $public_url"
curl -fsS -X PUT "$base_url/api/public-server-url" \
  -H "content-type: application/json" \
  --data "{\"publicServerUrl\":\"$public_url\"}" >/dev/null

echo "verifying public server URL setting"
settings_body="$(curl -fsS "$base_url/api/public-server-url")"
case "$settings_body" in
  *"\"publicServerUrl\":\"$public_url\""*) ;;
  *)
    echo "unexpected /api/public-server-url response:" >&2
    echo "$settings_body" >&2
    exit 1
    ;;
esac

echo "checking M3U endpoint"
m3u_body="$(curl -fsS "$base_url/api/m3u")"
if [[ "$m3u_body" != \#EXTM3U* ]]; then
  echo "M3U response did not start with #EXTM3U" >&2
  exit 1
fi

echo "checking XMLTV endpoint"
xmltv_body="$(curl -fsS "$base_url/api/xmltv")"
if [[ "$xmltv_body" != *"<tv"* ]]; then
  echo "XMLTV response did not contain <tv" >&2
  exit 1
fi

echo "ok: IPTV URLs"
echo "  M3U:   $public_url/api/m3u"
echo "  XMLTV: $public_url/api/xmltv"
