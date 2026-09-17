# Sourced by the other scripts. Loads .env so they agree with what Compose is
# actually running.
#
# Without this the scripts use their own built-in defaults while Compose uses
# .env, and the two silently disagree the moment anyone customises a port -
# the stack comes up correctly and the wait script times out against the wrong
# address.
#
# Precedence matches Compose: a variable already set in the environment wins
# over .env, so `HTTPS_PORT=9999 make verify` still does what it says.

_env_file="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/.env"

if [[ -f "$_env_file" ]]; then
    while IFS= read -r _line || [[ -n "$_line" ]]; do
        # Skip blanks and comments.
        [[ "$_line" =~ ^[[:space:]]*# ]] && continue
        [[ "$_line" =~ ^[[:space:]]*$ ]] && continue
        [[ "$_line" != *=* ]] && continue

        _key="${_line%%=*}"
        _key="${_key//[[:space:]]/}"
        _val="${_line#*=}"

        [[ "$_key" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]] || continue

        # Only set what the caller has not already set.
        if [[ -z "${!_key:-}" ]]; then
            export "$_key=$_val"
        fi
    done < "$_env_file"
fi

unset _env_file _line _key _val

# Pick a working Compose command unless the caller named one.
#
# `docker compose` (the v2 plugin) is the modern spelling and is tried first,
# but plenty of hosts still have only the standalone `docker-compose` binary,
# and on some setups `docker` is a shell alias that does not exist as an
# executable at all. Detecting beats assuming and failing obscurely later.
if [[ -z "${COMPOSE:-}" ]]; then
    if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
        COMPOSE="docker compose"
    elif command -v docker-compose >/dev/null 2>&1; then
        COMPOSE="docker-compose"
    else
        COMPOSE="docker compose"   # nothing found; fail with the familiar name
    fi
fi
export COMPOSE

# The container CLI itself, for the few places that need to run a throwaway
# container rather than drive the Compose project.
if [[ -z "${DOCKER:-}" ]]; then
    if command -v docker >/dev/null 2>&1; then
        DOCKER="docker"
    elif command -v podman >/dev/null 2>&1; then
        DOCKER="podman"
    else
        DOCKER="docker"
    fi
fi
export DOCKER
