#!/usr/bin/env bash
set -eu

if [ "${1:-serve}" != "serve" ]; then
  exec "$@"
fi

: "${LINEARCAST_ADDR:=:8888}"
: "${LINEARCAST_ENCODER_DIST_DIR:=/opt/linearcast/encoder-dist}"

# nginx proxies every dynamic route to the composed backend listener. Derive
# its port so a custom LINEARCAST_ADDR is honored end to end.
LC_PORT="${LINEARCAST_ADDR##*:}"
case "$LC_PORT" in
  ''|*[!0-9]*)
    echo "LINEARCAST_ADDR must end in a numeric :port (got '${LINEARCAST_ADDR}')" >&2
    exit 1 ;;
esac
export LINEARCAST_ADDR LINEARCAST_ENCODER_DIST_DIR LC_PORT

# Render the nginx config from its template, expanding only the backend port
# token so nginx's own $variables survive untouched.
envsubst '${LC_PORT}' \
  < /etc/nginx/nginx.conf.template \
  > /tmp/nginx/nginx.conf

if [ -z "${LINEARCAST_DB:-}" ]; then
  echo "LINEARCAST_DB is required" >&2
  exit 1
fi

echo "linearcast: running migrations"
linearcast-maint migrate

pids=()

start_service() {
  name="$1"
  shift
  echo "linearcast: starting ${name}"
  "$@" &
  pids+=("$!")
}

stop_services() {
  trap - INT TERM
  echo "linearcast: stopping services"
  local pid
  for pid in "${pids[@]}"; do
    kill "$pid" 2>/dev/null || true
  done
  for _ in {1..4}; do
    if [ -z "$(jobs -pr)" ]; then
      wait || true
      return
    fi
    sleep 1
  done
  echo "linearcast: forcing remaining services down"
  for pid in "${pids[@]}"; do
    kill -9 "$pid" 2>/dev/null || true
  done
  wait || true
}

trap 'stop_services; exit 0' INT TERM

start_service linearcast linearcast
start_service linearcast-extender linearcast-extender
start_service linearcast-encoder linearcast-encoder run
start_service nginx nginx -c /tmp/nginx/nginx.conf -e /dev/stderr -g "daemon off;"

set +e
wait -n
status="$?"
set -e
echo "linearcast: child process exited with status ${status}; shutting down" >&2
stop_services
exit "$status"
