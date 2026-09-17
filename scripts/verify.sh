#!/usr/bin/env bash
#
# End-to-end check of a running stack. This is the script that answers "is it
# actually working?" without anyone having to open a browser, and it is what
# the deploy pipeline would run as its post-deploy gate.
set -uo pipefail

source "$(dirname "${BASH_SOURCE[0]}")/load-env.sh"

HTTPS_PORT="${HTTPS_PORT:-8443}"
HTTP_PORT="${HTTP_PORT:-8080}"
PROM_PORT="${PROMETHEUS_PORT:-9090}"
BASE="https://localhost:${HTTPS_PORT}"

pass=0
fail=0

check() {
  local name="$1"; shift
  if "$@" >/dev/null 2>&1; then
    printf '  \033[32mPASS\033[0m  %s\n' "$name"
    pass=$((pass + 1))
  else
    printf '  \033[31mFAIL\033[0m  %s\n' "$name"
    fail=$((fail + 1))
  fi
}

# --- edge ---
echo "edge"
check "TLS handshake succeeds" \
  bash -c "echo | openssl s_client -connect localhost:${HTTPS_PORT} -servername localhost 2>/dev/null | grep -q 'BEGIN CERTIFICATE'"
check "HTTP redirects to HTTPS" \
  bash -c "curl -s -o /dev/null -w '%{http_code}' http://localhost:${HTTP_PORT}/ | grep -q 301"
check "health endpoint served through the proxy" \
  bash -c "curl -skf ${BASE}/healthz | grep -q '\"status\":\"ok\"'"
check "internal /metrics is NOT exposed at the edge" \
  bash -c "[ \"\$(curl -sk -o /dev/null -w '%{http_code}' ${BASE}/metrics)\" = 404 ]"
check "security headers present" \
  bash -c "curl -skI ${BASE}/healthz | grep -qi 'x-content-type-options: nosniff'"

# --- application ---
echo "application"
check "HTTP echo round-trips a payload" \
  bash -c "curl -skf -X POST ${BASE}/api/echo -H 'content-type: application/json' -d '{\"message\":\"verify\"}' | grep -q '\"message\":\"verify\"'"
# Checked with a real WebSocket client rather than curl. curl negotiates HTTP/2
# over TLS by ALPN, and WebSocket upgrade does not exist in HTTP/2 - nginx
# correctly answers 426 there - so a curl-based probe tests the wrong thing.
# Browsers have the same constraint and open WebSockets over HTTP/1.1.
check "WebSocket session completes through the edge" \
  bash -c "$COMPOSE --profile load run --rm --no-deps loadgen \
      -url=wss://nginx:8443/ws -connections=1 -rate=10 -duration=3s -ramp=0s 2>/dev/null \
    | python3 -c '
import re, sys
# Asserts only that messages round-tripped. Session errors are not a failure
# here: the demo injects faults on purpose, so a session closing mid-run is
# the stack working as configured.
out = sys.stdin.read()
done = re.search(r\"messages completed : (\\d+)\", out)
sys.exit(0 if done and int(done.group(1)) > 0 else 1)
'"

# --- observability ---
echo "observability"
check "all Prometheus targets are up" \
  bash -c "curl -sf localhost:${PROM_PORT}/api/v1/targets \
    | python3 -c 'import json,sys; t=json.load(sys.stdin)[\"data\"][\"activeTargets\"]; sys.exit(0 if t and all(x[\"health\"]==\"up\" for x in t) else 1)'"
check "service latency histogram has samples" \
  bash -c "curl -sf 'localhost:${PROM_PORT}/api/v1/query?query=sum(voice_echo_message_latency_seconds_count)' \
    | python3 -c 'import json,sys; r=json.load(sys.stdin)[\"data\"][\"result\"]; sys.exit(0 if r and float(r[0][\"value\"][1])>0 else 1)'"
check "recording rules are producing series" \
  bash -c "curl -sf 'localhost:${PROM_PORT}/api/v1/query?query=job:echo_latency_seconds:p99' \
    | python3 -c 'import json,sys; sys.exit(0 if json.load(sys.stdin)[\"data\"][\"result\"] else 1)'"
check "alert rules are loaded" \
  bash -c "curl -sf localhost:${PROM_PORT}/api/v1/rules \
    | python3 -c 'import json,sys; g=json.load(sys.stdin)[\"data\"][\"groups\"]; n=sum(len(x[\"rules\"]) for x in g); sys.exit(0 if n>=8 else 1)'"
check "edge metrics are being scraped" \
  bash -c "curl -sf 'localhost:${PROM_PORT}/api/v1/query?query=nginx_connections_active' \
    | python3 -c 'import json,sys; sys.exit(0 if json.load(sys.stdin)[\"data\"][\"result\"] else 1)'"

# --- dashboards ---
echo "dashboards"
check "Grafana is reachable through the edge" \
  bash -c "curl -skf ${BASE}/grafana/api/health | grep -q '\"database\": *\"ok\"'"
check "both dashboards are provisioned" \
  bash -c "curl -skf -u \"\${GRAFANA_ADMIN_USER:-admin}:\${GRAFANA_ADMIN_PASSWORD:-admin}\" \
    '${BASE}/grafana/api/search?type=dash-db' \
    | python3 -c 'import json,sys; d=json.load(sys.stdin); u={x[\"uid\"] for x in d}; sys.exit(0 if {\"realtime-latency\",\"realtime-errors\"} <= u else 1)'"
check "Grafana can query its datasource" \
  bash -c "curl -skf -u \"\${GRAFANA_ADMIN_USER:-admin}:\${GRAFANA_ADMIN_PASSWORD:-admin}\" \
    '${BASE}/grafana/api/datasources/uid/prometheus' | grep -q '\"type\": *\"prometheus\"'"

echo ""
if [[ $fail -eq 0 ]]; then
  printf '\033[32m%d checks passed\033[0m\n' "$pass"
  exit 0
fi
printf '\033[31m%d passed, %d failed\033[0m\n' "$pass" "$fail"
exit 1
