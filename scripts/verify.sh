#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

echo "==> Go vet"
go vet ./...

echo "==> Go tests"
go test ./...

echo "==> Web UI dependencies"
cd web-ui
npm ci

echo "==> UI API contract generation"
../scripts/check-api-contract.sh

echo "==> Web UI typecheck"
npm run typecheck

echo "==> Web UI tests"
npm test

echo "==> Web UI production build"
npm run build
