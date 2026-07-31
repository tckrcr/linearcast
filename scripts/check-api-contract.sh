#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
generated="$repo_root/web-ui/src/api/generated/schema.ts"
temporary="$(mktemp)"
trap 'rm -f "$temporary"' EXIT

"$repo_root/web-ui/node_modules/.bin/openapi-typescript" \
  "$repo_root/api/openapi.yaml" \
  -o "$temporary" >/dev/null

if ! cmp -s "$generated" "$temporary"; then
  echo "generated UI API types are stale; run: cd web-ui && npm run api:generate" >&2
  diff -u "$generated" "$temporary" || true
  exit 1
fi

if rg -n '\bfetch[[:space:]]*\(' \
  "$repo_root/web-ui/src" \
  --glob '*.ts' \
  --glob '*.tsx' \
  --glob '!**/api/client.ts'; then
  echo "UI requests must go through web-ui/src/api/client.ts" >&2
  exit 1
fi
