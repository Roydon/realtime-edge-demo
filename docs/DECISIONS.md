# Decisions

Why this stack looks the way it does. Each entry states the alternative that
was rejected and what it would cost, because a decision record that only lists
what was chosen is not a decision record.

---

## Nginx, not Caddy or Traefik

**Chosen:** Nginx as the TLS-terminating reverse proxy.

Caddy is genuinely easier. Automatic ACME, a five-line config, sane defaults -
for most projects it is the better answer, and choosing Nginx over it needs a
reason beyond familiarity.

The reason is operational rather than technical. Nginx's behaviour under load
is extremely well documented, and its failure modes are ones every operations
engineer has already seen: worker connection exhaustion, `no live upstreams`,
file descriptor limits. When something breaks at 3am on a platform where the
symptom is "calls are dropping", the value of a component whose failure modes
are widely understood outweighs the value of one that was pleasant to
configure. The tuning surface that matters here - `proxy_read_timeout` for
long-lived sessions, `worker_connections`, upstream keepalive, connection rate
limits - is directly exposed rather than abstracted away.

Traefik was rejected for a different reason: its strength is dynamic service
discovery in an orchestrated cluster. This deployment is a small number of
long-lived nodes with a static topology. Traefik's discovery machinery would
be complexity bought and not used, and it puts a control plane between a
request and the backend on a latency-sensitive path.

**What would change the decision:** moving to Kubernetes. There, an ingress
controller integrated with the cluster's service discovery is clearly correct,
and hand-managed Nginx config becomes the harder option.

---

## The upstream resolution bug, and why the config looks unusual

This is the most operationally important decision in the repository, and it
was found by breaking the running stack rather than by reasoning about it.

Nginx resolves a hostname in an `upstream` block **once**, at configuration
load, and caches the address for the lifetime of the process. Every deploy
that replaces the service container gives it a new address. Nginx keeps
sending traffic to the address that no longer exists, returns `no live
upstreams`, and 502s every request until somebody reloads it by hand.

On a voice platform that is every call dropped, on every release, until a human
notices. It does not show up in testing because it requires a container
replacement to trigger, and it does not show up in the application's own
metrics: during the failure Prometheus was still scraping the service happily
and every service-side metric looked healthy. Only the edge knew.

The fix is to configure an explicit `resolver` and pass the upstream through a
variable, which forces re-resolution on the `valid` interval:

```nginx
resolver ${DNS_RESOLVER} valid=10s ipv6=off;

location /ws {
    set $echo_upstream echo-service:8080;
    proxy_pass http://$echo_upstream;
}
```

**The cost is real.** A variable in `proxy_pass` bypasses the `upstream` block,
so upstream connection keepalive is lost - an extra TCP handshake per request
on the internal network. That is the right trade here: the keepalive saves
tens of microseconds on a path that is already sub-millisecond, and the bug it
prevents fails closed on the single operation that happens most often.

The resolver address itself is substituted at container start from the
container's own `/etc/resolv.conf`, by a small `.envsh` script in
`/docker-entrypoint.d/`. Docker uses `127.0.0.11`, Podman uses the network
gateway, and a plain VM uses the host resolver. Hardcoding any one of them
produces a config that works on its author's machine and 502s on everyone
else's - which is a worse failure than the one being fixed, because it is
invisible until someone else runs it.

**Alternative rejected:** reloading Nginx as part of every deploy. It works,
but it makes correctness depend on a step in a script that someone will
eventually reorder, and it does nothing for a container that restarts on its
own at 4am.

---

## Why the metrics are shaped this way

**Chosen:** a latency histogram, a session gauge, and counters split by cause
and by disconnect reason.

The temptation with a demo is to export a request counter and call it
monitoring. The four signals that actually matter for a real-time service are
latency distribution, session count, error rate, and *why* sessions end - and
the last one is the one most often missing.

Separating `client_closed` from `error` in the disconnect counter is the
difference between an alert that means something and an alert that pages
someone every time a user hangs up normally. A service that cannot distinguish
a normal hang-up from a dropped call cannot be alerted on at all, and there is
a test in `internal/echo/server_test.go` pinning that behaviour specifically
because it is the kind of thing a refactor silently breaks.

The histogram buckets are tuned to the domain: dense between 0.5ms and 250ms,
sparse above it. The default client_golang buckets top out in a range that is
meaningless here - past about 250ms of handling time, interactive audio is
already unusable, so the exact value stops mattering and only the fact of the
breach does.

Error counts are exported alongside message counts so the alert can be written
as a *ratio*. An absolute error rate is unreadable without traffic volume next
to it: 50 errors/sec is a crisis at 100 msg/sec and background noise at
100k msg/sec.

---

## Percentiles in recording rules, not in the dashboard

Both dashboards query pre-computed recording rules rather than evaluating
`histogram_quantile` in the panel.

Two reasons, both operational. A panel that evaluates a quantile over a wide
range on every refresh is one of the most common causes of a Grafana instance
that feels broken. More importantly, pinning the expression in one place means
the dashboard and the alert watching the same signal cannot drift apart - and
a dashboard that disagrees with the alert that woke someone up is worse than
no dashboard, because it costs time at exactly the wrong moment.

---

## What is exposed at the edge, and what is not

Grafana is proxied at `/grafana`. Prometheus is bound to loopback and is not
proxied at all. The service's `/metrics` endpoint returns 404 at the edge and
is reachable only on the internal monitoring network.

Prometheus has no authentication of any kind. Anything that can reach it can
read every metric, and with the lifecycle API enabled can also make it reload
its configuration. It belongs behind a VPN or an authenticating proxy, never
on a published port - and in this stack it sits on a network the edge proxy
cannot route to at all, so a proxy misconfiguration cannot expose it.

`/metrics` is not merely uninteresting to an attacker; it is a free
reconnaissance feed. Internal hostnames, service topology, version strings and
traffic volumes, unauthenticated.

---

## Two container networks

`edge` carries anything the proxy can reach. `monitoring` carries scrape
traffic. Prometheus sits only on `monitoring`.

The point is that this is a structural guarantee rather than a configuration
one. A mistake in the Nginx config cannot expose Prometheus, because there is
no route between them to misconfigure.

---

## Distroless images, and the cost of that

The service image is `distroless/static` running as a non-root user: no shell,
no package manager, no busybox. A compromised container has close to nothing to
work with.

**The cost is real and worth stating.** `docker exec <container> sh` does not
work. Debugging a running container requires attaching an ephemeral sidecar
that shares its namespaces. That is a genuine loss of convenience, traded for
a materially smaller blast radius on an internet-facing service.

It also forces the health check to be interesting: there is no `curl` to probe
with, so the binary probes itself (`echo-service -healthcheck`). That probe
deliberately hits `/healthz` and not `/readyz` - a draining instance is
intentionally not ready, and must not be restarted for it.

**Nginx is the exception.** It runs from the standard image, where the master
process starts as root and workers drop privileges. The unprivileged variant
was tried and rejected: it cannot read a certificate key with `600`
permissions, and the workaround is to loosen the key's permissions - trading a
real protection on the private key for a theoretical one on the process.

---

## Terraform is plan-only, with no backend

There is no backend block and CI never runs `apply`. The configuration exists
to show production intent and to be a starting point, not to manage anything.

A real deployment needs remote state with locking - DO Spaces or Terraform
Cloud - because two concurrent applies against unlocked state is how
infrastructure gets corrupted. Including a backend here would either point at
infrastructure that does not exist or invite someone to apply this against a
real account by accident.

Two guardrails are deliberate:

- `ssh_admin_cidrs` defaults to `192.0.2.0/24`, a documentation-only range, and
  a validation rule **rejects** `0.0.0.0/0` outright. A default that fails safe
  beats a comment asking people to be careful.
- The droplet's `lifecycle` block ignores `image` and `user_data` changes.
  Replacing a live media node drops every call on it; that has to be a
  deliberate blue/green rollout, not something that slips through in a plan
  nobody read closely.

---

## Tag-driven releases, digest-pinned deploys

Pushing to `main` builds and tests but ships nothing. Only a `v*.*.*` tag
produces an image, so the registry always maps to a commit someone deliberately
marked.

The deploy pins by **digest**, not by tag. A tag can be moved to point at
different content after it was tested; a digest cannot. This is what makes "we
deployed exactly what we tested" a true statement rather than an assumption,
and it is why `scripts/deploy.sh` rewrites one line in the host's `.env` rather
than editing the compose file - the compose file stays identical across every
environment.

The SSH step pins the host key from a secret rather than using
`StrictHostKeyChecking=no`. Disabling host verification means the pipeline will
happily hand credentials to whatever answers on that address.

The deploy defaults to a dry run and requires an explicit `DEPLOY_ENABLED`
repository variable to do anything real. This repository has no host to deploy
to, and a safe mode added as an afterthought is one nobody trusts enough to
test.

---

## Extending this to a real LiveKit/WebRTC deployment

The echo service is a stand-in. Everything around it - the edge, the metrics
pipeline, the alerting, the deploy path - is the part that carries over
unchanged. What changes when a real SFU replaces it:

### Media ports are the main structural difference

WebSocket traffic is one TCP connection per session through the proxy. WebRTC
media is not: an SFU allocates a **UDP port per participant connection**,
typically from a range like `50000-60000`, and that traffic must reach the SFU
process directly.

Consequences:

1. **Media does not go through Nginx.** Nginx terminates TLS for signalling and
   the HTTP API. Media bypasses it entirely. A reverse proxy in the media path
   would add latency to the one thing that cannot absorb it.
2. **The UDP range must be open to the world.** There is no source address to
   restrict it to - arbitrary clients on arbitrary networks are the point.
   `terraform/modules/edge-node` has this wired behind `enable_media_ports`,
   off by default because the echo service needs none of it.
3. **The range size is a hard concurrency ceiling** for the node. Sizing it is
   a capacity decision, not a default to copy.
4. **The container cannot use bridge networking.** Publishing ten thousand UDP
   ports through a NAT layer is unworkable; the SFU runs with host networking,
   which in turn means port conflicts become a real constraint on what else
   runs on that node.

### TURN is not optional

Most clients connect peer-to-peer or directly to the SFU. A minority - behind
symmetric NAT, or corporate and hotel firewalls that block UDP - cannot, and
need media relayed through TURN (3478/udp, 5349/tcp).

This is worth emphasising because of how it fails. Without TURN those users
connect successfully, signalling completes, the session appears healthy in
every server-side metric, and they hear silence. It is the hardest WebRTC
failure to diagnose from the server side precisely because nothing on the
server looks wrong. Budget for TURN bandwidth: relayed media is real egress,
and it is billed.

A TCP fallback on 443 catches clients whose network blocks UDP entirely.
Materially worse for quality, and much better than a call that never connects.

### What the monitoring becomes

The metric *shape* transfers directly; the specific metrics change:

| This demo | Real deployment |
|---|---|
| `voice_echo_active_sessions` | participants per room, rooms per node |
| `voice_echo_message_latency_seconds` | jitter, RTT, packet loss per track |
| `voice_echo_errors_total` | ICE failure rate by candidate type |
| `voice_echo_disconnects_total` | session end reason, TURN relay share |

LiveKit exports Prometheus metrics natively, so `prometheus.yml` gains a scrape
job and the dashboards are rebuilt on the same recording-rule pattern.

The two alerts that matter most on a real deployment have no equivalent here:
**ICE failure rate** (clients that cannot establish media at all - the silent
failure above) and **TURN relay share** (a sudden rise means a network path
degraded and costs are about to rise with it).

### What stays exactly as it is

The CI/CD pipeline, the digest-pinned deploy with health check and rollback,
the firewall module, the TLS edge for signalling and API traffic, the recording
rule and alerting structure, the runbook, and the backup approach. That is the
point of the demo: the stand-in is the small part.

### Capacity, briefly

An SFU is bandwidth- and CPU-bound long before it is memory-bound. Sizing
starts from concurrent participants multiplied by per-participant bitrate, and
egress cost tends to dominate the bill well before compute does - which makes
region choice (`terraform/variables.tf`) the highest-leverage decision in the
whole configuration, since it sets the propagation floor that no amount of
tuning recovers.
