#!/usr/bin/env bash
#
# Deploys a released image to the target host over SSH.
#
# Defaults to a dry run, which prints the exact commands that would run on the
# host and changes nothing. That default is deliberate: this repository is a
# demonstration and has no host to deploy to, and a deploy script whose safe
# mode is an afterthought is a deploy script nobody trusts to test.
#
# Required when DRY_RUN is not "true":
#   IMAGE, DIGEST, VERSION, DEPLOY_HOST, DEPLOY_USER
set -euo pipefail

DRY_RUN="${DRY_RUN:-true}"
IMAGE="${IMAGE:-ghcr.io/example/echo-service}"
DIGEST="${DIGEST:-sha256:0000000000000000000000000000000000000000000000000000000000000000}"
VERSION="${VERSION:-v0.0.0}"
DEPLOY_HOST="${DEPLOY_HOST:-realtime-edge.example.com}"
DEPLOY_USER="${DEPLOY_USER:-deploy}"
REMOTE_DIR="${REMOTE_DIR:-/opt/realtime-edge}"

# Pinning by digest, not by tag. A tag can be moved to point at different
# content after it was tested; a digest cannot. This is what makes "the same
# thing we tested" a true statement rather than an assumption.
PINNED_IMAGE="${IMAGE}@${DIGEST}"

read -r -d '' REMOTE_SCRIPT <<REMOTE || true
set -euo pipefail
cd "${REMOTE_DIR}"

# Record what is running now, so the rollback script has somewhere to go back
# to. Capturing this after pulling the new image would be too late.
docker compose ps --format '{{.Image}}' > .previous-image || true

echo "pulling ${PINNED_IMAGE}"
docker pull "${PINNED_IMAGE}"

# Write the new image into the environment file the compose stack reads,
# rather than editing the compose file itself. The compose file stays identical
# across environments and only this one line differs per release.
sed -i "s|^ECHO_IMAGE=.*|ECHO_IMAGE=${PINNED_IMAGE}|" .env

# --no-deps so the edge and the monitoring stack are not restarted for an
# application release. Restarting nginx here would drop every live session for
# a change that does not affect it.
docker compose up -d --no-deps --wait --wait-timeout 120 echo-service

docker compose ps
echo "deployed ${VERSION} (${DIGEST})"

# Old image layers accumulate until the disk fills. Pruning only dangling
# images leaves tagged rollback targets in place.
docker image prune -f --filter "dangling=true"
REMOTE

if [[ "$DRY_RUN" == "true" ]]; then
  cat <<BANNER

  ==========================================================================
   DRY RUN - nothing will be changed on any host.

   Set the repository variable DEPLOY_ENABLED=true and provide the secrets
   DEPLOY_SSH_KEY, DEPLOY_KNOWN_HOSTS, DEPLOY_HOST and DEPLOY_USER to run
   this for real.
  ==========================================================================

  target      : ${DEPLOY_USER}@${DEPLOY_HOST}
  remote dir  : ${REMOTE_DIR}
  image       : ${PINNED_IMAGE}
  version     : ${VERSION}

  would run over SSH:
  --------------------------------------------------------------------------
BANNER
  printf '%s\n' "$REMOTE_SCRIPT" | sed 's/^/  | /'
  cat <<'BANNER'
  --------------------------------------------------------------------------

  followed by a health check against https://<host>/healthz, and an automatic
  rollback to the recorded previous image if it does not pass.

BANNER
  exit 0
fi

echo "deploying ${PINNED_IMAGE} to ${DEPLOY_USER}@${DEPLOY_HOST}"

# BatchMode so a missing key fails immediately instead of hanging on a
# password prompt until the job times out.
ssh -o BatchMode=yes -o ConnectTimeout=10 \
  "${DEPLOY_USER}@${DEPLOY_HOST}" "bash -s" <<< "$REMOTE_SCRIPT"

echo "deploy complete"
