#!/usr/bin/env bash
# Manage the linearcast-encoder as a local Linux systemd service.
# The binary connects to the local admin HTTP endpoint using bearer auth,
# exercising the full remote encoder code path without a separate machine.
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/local-encoder-service.sh <install|uninstall|update> [options]

Commands:
  install     Build binary, write systemd unit, enable and start service.
  uninstall   Stop, disable, and remove the service unit and env file.
              Binary and work directory are intentionally preserved.
  update      Rebuild binary and restart the running service.

Options:
  --admin-url <url>      Admin URL. Default: LINEARCAST_ADMIN_URL from env file
  --api-key <key>        Encoder API key (lcenc_...).
                         Default: LINEARCAST_ENCODER_API_KEY from env file
  --concurrency <n>      LINEARCAST_ENCODER_CONCURRENCY override. Optional;
                         falls back to the value set on the encoder row.
  --service-name <name>  Systemd service name. Default: linearcast-encoder-local
  --install-dir <dir>    Binary install directory. Default: /usr/local/bin
  --work-dir <dir>       Encoder work directory.
                         Default: /var/lib/linearcast-encoder-local/work
  --env-file <path>      Local env source for defaults.
                         Default: .env.linearcast-encoder-local
  -h|--help              Show this help.

Env file (.env.linearcast-encoder-local):
  LINEARCAST_ADMIN_URL=http://localhost:8080
  LINEARCAST_ENCODER_API_KEY=lcenc_...
  LINEARCAST_ENCODER_CONCURRENCY=2   # optional

The encoder must be registered in the admin UI before running install.
EOF
}

# ── defaults ────────────────────────────────────────────────────────────────

service_name="linearcast-encoder-local"
install_dir="/usr/local/bin"
work_dir="/var/lib/linearcast-encoder-local/work"
env_file=".env.linearcast-encoder-local"
admin_url=""
api_key=""
concurrency=""

# ── parse command ────────────────────────────────────────────────────────────

if [[ $# -lt 1 ]]; then usage >&2; exit 2; fi
command="$1"; shift

case "$command" in
  install|uninstall|update) ;;
  -h|--help) usage; exit 0 ;;
  *) echo "Unknown command: $command" >&2; usage >&2; exit 2 ;;
esac

while [[ $# -gt 0 ]]; do
  case "$1" in
    -h|--help) usage; exit 0 ;;
    --admin-url)     shift; admin_url="${1:?--admin-url requires a value}" ;;
    --api-key)       shift; api_key="${1:?--api-key requires a value}" ;;
    --concurrency)   shift; concurrency="${1:?--concurrency requires a value}" ;;
    --service-name)  shift; service_name="${1:?--service-name requires a value}" ;;
    --install-dir)   shift; install_dir="${1:?--install-dir requires a value}" ;;
    --work-dir)      shift; work_dir="${1:?--work-dir requires a value}" ;;
    --env-file)      shift; env_file="${1:?--env-file requires a value}" ;;
    *) echo "Unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
  shift
done

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

# Load env file if present (provides defaults for admin_url/api_key).
if [[ -f "$env_file" ]]; then
  # shellcheck disable=SC1090
  source "$env_file"
fi
admin_url="${admin_url:-${LINEARCAST_ADMIN_URL:-}}"
api_key="${api_key:-${LINEARCAST_ENCODER_API_KEY:-}}"
concurrency="${concurrency:-${LINEARCAST_ENCODER_CONCURRENCY:-}}"

# ── helpers ──────────────────────────────────────────────────────────────────

step() { printf '\n==> %s\n' "$*"; }

# Prefix privileged writes/commands with sudo when not already root.
if [[ $EUID -eq 0 ]]; then
  SUDO=""
else
  SUDO="sudo"
fi

unit_file="/etc/systemd/system/${service_name}.service"
svc_env_file="/etc/${service_name}.env"
binary="${install_dir}/linearcast-encoder"
built_binary="dist/linearcast-encoder-local"
service_user="$(id -un)"

# ── build ────────────────────────────────────────────────────────────────────

# Build into dist/ as the current user (go may not be in sudo's PATH),
# then sudo-install from there into the target directory.
build_binary() {
  step "Building linearcast-encoder"
  mkdir -p dist
  CGO_ENABLED=0 go build -o "$built_binary" ./cmd/linearcast-encoder
  echo "    built: $repo_root/$built_binary"
}

# ── commands ─────────────────────────────────────────────────────────────────

do_install() {
  if [[ -z "$admin_url" ]]; then
    echo "Error: --admin-url is required (or set LINEARCAST_ADMIN_URL in $env_file)" >&2
    exit 1
  fi
  if [[ -z "$api_key" ]]; then
    echo "Error: --api-key is required (or set LINEARCAST_ENCODER_API_KEY in $env_file)" >&2
    echo "       Register the encoder in the admin UI first to get a key." >&2
    exit 1
  fi

  build_binary

  step "Installing binary to $install_dir"
  $SUDO install -m 0755 "$built_binary" "$install_dir/linearcast-encoder"

  step "Creating work directory $work_dir"
  $SUDO mkdir -p "$work_dir"
  $SUDO chown "$service_user" "$work_dir"

  step "Writing env file $svc_env_file"
  {
    echo "LINEARCAST_ADMIN_URL=$admin_url"
    echo "LINEARCAST_ENCODER_API_KEY=$api_key"
    echo "LINEARCAST_ENCODER_WORK_DIR=$work_dir"
    if [[ -n "$concurrency" ]]; then
      echo "LINEARCAST_ENCODER_CONCURRENCY=$concurrency"
    fi
  } | $SUDO tee "$svc_env_file" > /dev/null
  $SUDO chmod 0640 "$svc_env_file"

  step "Writing systemd unit $unit_file"
  $SUDO tee "$unit_file" > /dev/null <<EOF
[Unit]
Description=Linearcast encoder (local dev)
After=network.target

[Service]
Type=simple
User=$service_user
EnvironmentFile=$svc_env_file
ExecStart=$install_dir/linearcast-encoder run
Restart=on-failure
RestartSec=10
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
EOF

  step "Enabling and starting $service_name"
  $SUDO systemctl daemon-reload
  $SUDO systemctl enable --now "$service_name"

  echo ""
  echo "Service started. Useful commands:"
  echo "  journalctl -fu $service_name        # tail logs"
  echo "  systemctl status $service_name      # status"
  echo "  systemctl stop $service_name        # pause"
  echo "  systemctl start $service_name       # resume"
  echo "  $0 update                           # rebuild and restart"
  echo "  $0 uninstall                        # tear down"
}

do_uninstall() {
  step "Stopping and disabling $service_name"
  if systemctl is-active --quiet "$service_name" 2>/dev/null; then
    $SUDO systemctl stop "$service_name"
  fi
  if systemctl is-enabled --quiet "$service_name" 2>/dev/null; then
    $SUDO systemctl disable "$service_name"
  fi

  step "Removing unit and env files"
  $SUDO rm -f "$unit_file" "$svc_env_file"
  $SUDO systemctl daemon-reload

  echo ""
  echo "Service removed. Binary ($binary) and work dir ($work_dir) preserved."
  echo "To clean those up manually:"
  echo "  sudo rm $binary"
  echo "  sudo rm -rf $work_dir"
}

do_update() {
  if ! systemctl is-active --quiet "$service_name" 2>/dev/null; then
    echo "Warning: $service_name is not currently running. Will install binary anyway." >&2
  fi

  build_binary

  step "Installing updated binary to $install_dir"
  $SUDO install -m 0755 "$built_binary" "$install_dir/linearcast-encoder"

  if systemctl is-active --quiet "$service_name" 2>/dev/null || \
     systemctl is-enabled --quiet "$service_name" 2>/dev/null; then
    step "Restarting $service_name"
    $SUDO systemctl restart "$service_name"
    echo "    done."
    echo ""
    echo "To view logs: journalctl -fu $service_name"
  fi
}

# ── dispatch ─────────────────────────────────────────────────────────────────

case "$command" in
  install)   do_install ;;
  uninstall) do_uninstall ;;
  update)    do_update ;;
esac
