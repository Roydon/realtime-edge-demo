#!/usr/bin/env bash
#
# Restores a snapshot produced by scripts/backup.sh.
#
# This exists to be run routinely, not only during an incident. The restore
# path is the half of a backup strategy that usually goes untested, and it is
# the half that matters.
set -euo pipefail

cd "$(dirname "${BASH_SOURCE[0]}")/.."

source "$(dirname "${BASH_SOURCE[0]}")/load-env.sh"
SNAPSHOT="${1:-}"

if [[ -z "$SNAPSHOT" ]]; then
  echo "usage: make restore SNAPSHOT=backups/realtime-edge-<stamp>.tar.gz" >&2
  echo "" >&2
  echo "available snapshots:" >&2
  ls -1t backups/*.tar.gz 2>/dev/null | sed 's/^/  /' >&2 || echo "  (none)" >&2
  exit 2
fi

if [[ ! -f "$SNAPSHOT" ]]; then
  echo "no such snapshot: $SNAPSHOT" >&2
  exit 1
fi

PROJECT="$($COMPOSE config --format json 2>/dev/null | python3 -c 'import json,sys; print(json.load(sys.stdin).get("name","realtime-edge-demo"))' 2>/dev/null || echo realtime-edge-demo)"

STAGING="$(mktemp -d)"
trap 'rm -rf "$STAGING"' EXIT
tar xzf "$SNAPSHOT" -C "$STAGING"

echo "restoring from ${SNAPSHOT}"
echo ""
sed 's/^/  /' "${STAGING}/MANIFEST.txt" 2>/dev/null || true
echo ""

# Restoring into volumes that a running container holds open produces a
# corrupted result that looks like it worked, which is the worst outcome
# available. Stop first.
echo "stopping the stack before touching its volumes"
$COMPOSE --profile load down

for vol in prometheus-data grafana-data; do
  archive="${STAGING}/${vol}.tar.gz"
  [[ -f "$archive" ]] || { echo "  skipping ${vol}: not in this snapshot"; continue; }

  full="${PROJECT}_${vol}"
  echo "  restoring ${full}"
  # Idempotent under Docker, an error under Podman when the volume already
  # exists - which is the normal case for a restore. Either way the mount in
  # the next step is what actually has to succeed, and it fails loudly.
  $DOCKER volume create "$full" >/dev/null 2>&1 || true

  # The target is emptied first. Untarring over existing data leaves whatever
  # the archive does not contain in place, producing a state that matches
  # neither the backup nor what was there before.
  $DOCKER run --rm \
    -v "${full}:/target" \
    -v "${STAGING}:/backup:ro" \
    alpine:3.21 \
    sh -c "rm -rf /target/* /target/..?* /target/.[!.]* 2>/dev/null; tar xzf /backup/${vol}.tar.gz -C /target"
done

echo ""
echo "restore complete. bring the stack back up with:"
echo "  make up && make verify"
