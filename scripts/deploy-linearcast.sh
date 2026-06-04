#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/deploy-linearcast.sh [--localremote]

Build and run the single-container linearcast service. The container starts
playback, admin API, schedule extender, local packager worker, and nginx/web UI.

Options:
  --localremote   After the main deploy, rebuild and restart the local
                  linearcast-encoder systemd service. The encoder must already
                  be registered (key stored in .env.linearcast-encoder-local).
                  Equivalent to running: scripts/local-encoder-service.sh update
EOF
}

deploy_local_encoder=false

while [[ $# -gt 0 ]]; do
  case "$1" in
    -h|--help) usage; exit 0 ;;
    --localremote) deploy_local_encoder=true ;;
    *) echo "Unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
  shift
done

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

export HOST_UID="${HOST_UID:-$(id -u)}"
export HOST_GID="${HOST_GID:-$(id -g)}"

echo "Deploying single-container linearcast from: $repo_root"

if [[ ! -f .env ]]; then
  cp deploy/.env.example .env
  echo "Created $(pwd)/.env; edit it before running this deploy again." >&2
  exit 1
fi

# Compose auto-reads .env for ${VAR} interpolation and as the env_file; we also
# source it here so this script's own preflight checks see the same values.
set -a
# shellcheck disable=SC1091
source .env
set +a

derive_port() {
  local explicit_port="$1"
  local addr="$2"
  local fallback="$3"
  if [[ -n "$explicit_port" ]]; then
    echo "$explicit_port"
    return
  fi
  if [[ "$addr" =~ :([0-9]+)$ ]]; then
    echo "${BASH_REMATCH[1]}"
    return
  fi
  echo "$fallback"
}

web_ui_port="${WEB_UI_PORT:-8080}"
linearcast_port="$(derive_port "${LINEARCAST_PORT:-}" "${LINEARCAST_ADDR:-}" 8888)"

require_env_path() {
  local name="$1"
  local value="${!name:-}"
  if [[ -z "$value" ]]; then
    echo "$name is not set in .env." >&2
    exit 1
  fi
  if [[ "$value" != /* ]]; then
    echo "$name must be an absolute host path; got: $value" >&2
    exit 1
  fi
}

require_existing_dir() {
  local name="$1"
  local value="${!name}"
  local purpose="$2"
  if [[ ! -d "$value" ]]; then
    cat >&2 <<EOF
$name does not exist or is not mounted: $value

This path is used for $purpose. Host bind paths are interpolated from
$(pwd)/.env (identity-mapped to the same in-container path). Docker Compose
validates bind-mount sources before starting containers, so the deploy cannot
continue until the host path exists.

If this is an external drive, mount it and rerun the deploy. If the path
changed, update $name in $(pwd)/.env.
EOF
    exit 1
  fi
  if [[ ! -r "$value" || ! -x "$value" ]]; then
    cat >&2 <<EOF
$name is not readable/traversable by $(id -un): $value

Fix the host permissions, or update $name in $(pwd)/.env before rerunning.
EOF
    exit 1
  fi
}

ensure_dir() {
  local name="$1"
  local value="${!name}"
  if [[ ! -d "$value" ]]; then
    mkdir -p "$value" || {
      echo "Failed to create $name=$value" >&2
      exit 1
    }
    echo "Created $name: $value"
  fi
  if [[ ! -r "$value" || ! -x "$value" ]]; then
    echo "$name is not readable/traversable: $value" >&2
    exit 1
  fi
}

ensure_db_parent() {
  local name="$1"
  local value="${!name}"
  local dir
  dir="$(dirname "$value")"
  if [[ ! -d "$dir" ]]; then
    mkdir -p "$dir" || {
      echo "Failed to create parent directory for $name=$value" >&2
      exit 1
    }
    echo "Created parent directory for $name: $dir"
  fi
  if [[ ! -r "$dir" || ! -x "$dir" ]]; then
    echo "Parent directory of $name is not readable/traversable: $dir" >&2
    exit 1
  fi
}

require_env_path LINEARCAST_DB
require_env_path CACHE_DIR
require_env_path LINEARCAST_MEDIA_ROOT
ensure_db_parent LINEARCAST_DB
ensure_dir CACHE_DIR
require_existing_dir LINEARCAST_MEDIA_ROOT "media library reads"

if ! docker version >/dev/null 2>&1; then
  echo "Docker is not available to $(id -un). Add this user to the docker group or run the deploy with a user that can access /var/run/docker.sock." >&2
  exit 1
fi

docker compose build linearcast
docker compose up -d --no-build --remove-orphans linearcast

sleep 2

base_url="http://localhost:${web_ui_port}"
scripts/release-smoke.sh \
  --web-base-url "$base_url" \
  --playback-base-url "$base_url" \
  --admin-api-url "$base_url"

host_ip="$(hostname -I 2>/dev/null | awk '{print $1}')"
[[ -z "$host_ip" ]] && host_ip="localhost"

echo "Deploy complete. Access the web UI at: http://${host_ip}:${web_ui_port}"

if [[ "$deploy_local_encoder" == true ]]; then
  echo ""
  echo "==> Updating local encoder service"
  scripts/local-encoder-service.sh update
fi

echo ""
echo "To view logs: docker compose logs -f linearcast"
