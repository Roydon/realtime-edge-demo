# Runbook

Procedures for the alerts this stack defines. Every alert carries a `runbook`
annotation pointing at a section here.

Written to be followed by someone who did not build this and was woken up by
it. That means: what to check, in what order, and what to do about it - not an
explanation of how the system works.

## First, always

```bash
make ps                # what is actually running
make alerts           # what is currently firing
docker compose logs --tail=100 echo-service
```

Two questions before anything else:

1. **Did something change?** Check recent deploys. A correlation between an
   alert and a release is the answer most of the time, and rolling back is
   faster than diagnosing.
2. **Is it the service or the edge?** `voice_echo_*` metrics come from the
   service; `nginx_*` metrics come from the proxy. If the service metrics look
   healthy and users are complaining, the problem is in front of the service -
   and the application's own dashboards will show nothing wrong.

---

## service-down

**Alert:** `EchoServiceDown` - Prometheus cannot scrape the service.

New sessions are failing. This is user-visible immediately.

```bash
docker compose ps echo-service          # running? restarting in a loop?
docker compose logs --tail=200 echo-service
curl -sk https://localhost:8443/healthz  # is it the service or the scrape path?
```

Read the result:

- **Container is restarting repeatedly** - almost always a configuration error.
  The service exits non-zero on malformed config by design rather than falling
  back to a default. The log line names the variable.
- **Container is up but not scraped** - the scrape path is broken, not the
  service. Check that Prometheus and the service are both on the `monitoring`
  network: `docker compose exec prometheus wget -qO- echo-service:8080/healthz`.
- **Container is up and healthy, alert still firing** - check Prometheus itself
  (`http://localhost:9090/targets`). An alert about a missing target can be an
  alert about a broken monitoring stack.

**Recovery:** `docker compose up -d --force-recreate echo-service`. If it was a
bad release, roll back instead - see [rollback](#rollback).

---

## edge-down

**Alert:** `EdgeProxyDown` - the Nginx exporter is unreachable.

If the proxy itself is down, every external client is failing TLS and *no
application metric will show it*. The service will look perfectly healthy while
no user can reach it.

```bash
docker compose ps nginx nginx-exporter
docker compose logs --tail=100 nginx
curl -sk -o /dev/null -w '%{http_code}\n' https://localhost:8443/healthz
```

- **Nginx is up, exporter is down** - metrics-only problem. Real user traffic is
  fine. Restart the exporter; do not touch Nginx.
- **Nginx will not start** - almost always a config or certificate error:
  `docker compose logs nginx | tail -30`. Validate with
  `docker compose exec nginx nginx -t`.
- **Certificate expired** - `make certs` regenerates the demo certificate, then
  `docker compose restart nginx`. In production this means the ACME renewal
  failed, and that is the thing to fix.
- **`no live upstreams` in the log** - see
  [502s after a deploy](#502s-after-a-deploy) below. This is a known failure
  mode with a specific cause.

---

## high-latency

**Alerts:** `EchoLatencyP99High` (>250ms, 2m) / `EchoLatencyP99Critical` (>1s, 1m).

Open the **Realtime Edge - Latency** dashboard and read p50 against p99 before
doing anything else. The shape tells you where to look:

- **p99 rose, p50 flat** - a subset of sessions is suffering, not the service as
  a whole. Look for one bad node, one large room, or one slow dependency. The
  latency heatmap will usually show two distinct bands.
- **p50 and p99 both rose** - the whole service is degraded. Check saturation:
  active sessions against the configured ceiling, then CPU and memory.
- **Both rose at a deploy boundary** - roll back first, diagnose afterwards.

```bash
# Is this load, or is it the code?
curl -s localhost:9090/api/v1/query?query='sum(rate(voice_echo_messages_total[1m]))'
curl -s localhost:9090/api/v1/query?query='voice_echo_active_sessions'
docker stats --no-stream
```

If traffic rose proportionally, this is capacity - see [capacity](#capacity).
If latency rose without traffic rising, it is the service or something it
depends on.

**Demo-specific:** `PROCESSING_JITTER` in `.env` injects synthetic latency. If
this alert fires on a demo stack, check that first - it is the intended cause.

---

## high-error-rate

**Alert:** `EchoErrorRateHigh` - errors above 5% of inbound messages for 2m.

Open **Realtime Edge - Errors and Saturation** and read the *errors by cause*
panel. The cause label decides the next action, and they are not
interchangeable:

| Cause | Meaning | Action |
|---|---|---|
| `injected` | The demo's own fault generator | Check `ERROR_INJECTION_RATE`. Not a real fault. |
| `read` / `write` | Transport faults | Network path or client-side problem. Check the edge. |
| `overloaded` | Load shedding at the session ceiling | [capacity](#capacity) |
| `bad_request` | Malformed client input | A client deployed something broken. Correlate with client releases. |

Then check the *disconnects by reason* panel. A rise in `client_closed` without
a rise in `error` is users hanging up, which is not an incident. The two are
deliberately counted separately for exactly this reason.

---

## capacity

**Alert:** `EchoSessionsRejected` - new sessions are being refused.

Load shedding is working as designed, and real users are being turned away. The
ceiling is doing its job; the ceiling is now the problem.

```bash
curl -s localhost:9090/api/v1/query?query='voice_echo_active_sessions'
grep MAX_SESSIONS .env
docker stats --no-stream
```

- **Sessions at the ceiling, resources have headroom** - the ceiling is set too
  low. Raise `MAX_SESSIONS` and redeploy.
- **Sessions at the ceiling, resources saturated** - the ceiling is correct and
  the node is full. Add capacity; do not raise the limit, or the failure mode
  changes from clean rejection to degradation for everyone already connected.
- **Sessions well below the ceiling but rejections are happening** - a leak.
  Sessions are being counted and not released. Check `voice_echo_active_sessions`
  against `rate(voice_echo_sessions_total)` over a long window.

---

## 502s after a deploy

Not an alert, but the most likely failure this stack will actually see, and the
one with a non-obvious cause.

**Symptom:** every request returns 502. Nginx logs `no live upstreams`. The
service container is healthy and Prometheus is scraping it without trouble.

**Cause:** Nginx cached the old container's IP address. This is fixed in this
repository (`nginx/templates/edge.conf.template` uses an explicit `resolver`
with the upstream behind a variable), but it is worth recognising because it is
the default behaviour of any Nginx config using a plain `upstream` block, and
it is *invisible in application metrics* - during the failure the service looks
completely healthy from the inside.

**Immediate fix:** `docker compose restart nginx`.

**Permanent fix:** already applied here. See `docs/DECISIONS.md`.

---

## rollback

When a release is the suspect, roll back before diagnosing. The investigation is
easier without users on the broken version.

```bash
# On the host:
cd /opt/realtime-edge
cat .previous-image            # recorded by the deploy before it changed anything
sed -i "s|^ECHO_IMAGE=.*|ECHO_IMAGE=<previous-digest>|" .env
docker compose up -d --no-deps --wait echo-service
curl -sf https://localhost/healthz
```

The release pipeline does this automatically when its post-deploy health check
fails. This is the manual path for when the problem is found later.

Because deploys pin by digest, rolling back is exact - the previous digest is
the previous bytes, not whatever a tag now points at.

---

## backup and restore

**What is worth backing up, and what is not.**

The service is stateless: it holds sessions in memory and nothing else.
Restoring it means starting the container. There is nothing to back up.

What carries state:

- **Prometheus TSDB** - metric history. Losing it means losing the ability to
  compare against last week, which matters for capacity work and for
  post-incident analysis. It is not worth an expensive backup strategy: 7-day
  retention is configured, and metric history is the most replaceable data here.
- **Grafana database** - users, API keys, and any dashboard edited in the UI.
  Dashboards and datasources are provisioned from the repository and are
  restored by redeploying, so the only irreplaceable content is what someone
  created by hand.

```bash
make backup                                # snapshot both volumes into ./backups
make restore SNAPSHOT=backups/<file>.tar.gz
```

**The part that actually matters:** a backup that has never been restored is a
hypothesis, not a backup. `make restore` exists so the restore path is exercised
routinely rather than discovered during an incident. Restore into a scratch
environment on a schedule and confirm the dashboards render.

For a real deployment, the important state is elsewhere entirely - the database
behind the application, and the TLS certificates and secrets. Those need a
tested restore procedure with a stated RPO and RTO, which this demo stack does
not model because it has neither.
