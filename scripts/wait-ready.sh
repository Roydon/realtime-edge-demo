#!/usr/bin/env bash
#
# Blocks until the edge is serving. `make demo` uses this so the printed URLs
# are live by the time the user reads them, rather than 404ing for the first
# few seconds and looking broken.
set -euo pipefail

source "$(dirname "${BASH_SOURCE[0]}")/load-env.sh"

HTTPS_PORT="${HTTPS_PORT:-8443}"
TIMEOUT="${WAIT_TIMEOUT:-90}"

deadline=$(( $(date +%s) + TIMEOUT ))
printf 'waiting for the edge on https://localhost:%s ' "$HTTPS_PORT"

while true; do
  # -k because the demo certificate is self-signed by design.
  if curl -skf --max-time 2 "https://localhost:${HTTPS_PORT}/healthz" >/dev/null 2>&1; then
    echo " ready"
    exit 0
  fi
  if [[ $(date +%s) -ge $deadline ]]; then
    echo " timed out after ${TIMEOUT}s"
    echo "check container status with: docker compose ps" >&2
    exit 1
  fi
  printf '.'
  sleep 2
done
