# Deployment

diagd is designed to be deployed as a fleet of small, disposable instances
rather than one large server. This follows the Broadband Forum's own test
architecture: TR-143 places Network Test Servers at the POP and further
tiers toward peering points, and TR-471 Annex C defines multi-flow testing
against a list of server instances. This document gives reference designs
from a single box to a distributed fleet.

## Ports and firewalling

| Listener | Default | Protocol | Exposure |
|---|---|---|---|
| TR-143 HTTP | `:8080` | TCP | subscribers |
| UDP Echo Plus | `:9000` | UDP | subscribers |
| TR-471 control | `:24601` | UDP | subscribers |
| TR-471 test connections | ephemeral, or `-tr471-test-ports min-max` | UDP | subscribers |
| ops (`/metrics`, `/healthz`) | `:9143` | TCP | management only |

Two TR-471 specifics matter for firewalls:

- Each test runs on a new server-side UDP port. With stateful firewalls,
  rely on the server's outbound null request to open the pinhole, or pin
  the range with `-tr471-test-ports` and open it explicitly. A fixed range
  is also easier to police and to steer.
- Clients behind NAT work because the client keeps one socket for the whole
  exchange and the server's null request opens the return path early. Port
  address translation that rewrites per-destination can still break the
  reference protocol; that is a property of the protocol, not of diagd.

diagd sends the null request before the setup response. The reference
server does the opposite, which exposes a kernel connection-tracking race
where the client's activation packet can be dropped with EPERM when both
directions of the new flow are inserted concurrently. If you place a
stateful firewall between clients and servers, prefer stateless rules for
the test port range regardless.

ICMP echo must reach test servers. TR-143 ServerSelectionDiagnostics picks
the target server by ping time; rate-limited or filtered ICMP skews server
selection for every device using it.

## Sizing and admission control

A test server must never be the bottleneck it is asked to measure. Size
each instance so its worst-case concurrent load fits inside the capacity
behind it, then enforce that with admission control:

- `-tr471-bandwidth <mbps>` caps admitted TR-471 load per direction.
  Clients state their provisioned rate (`udpst -B`), the server refuses
  what does not fit, and the CPE reports the refusal to its controller,
  which can retry elsewhere or later.
- `-max-transfers <n>` bounds concurrent HTTP test transfers; excess
  requests get 503 instead of degrading running tests. A multi-connection
  test uses one slot per TCP connection, so set this comfortably above
  the largest NumberOfConnections you provision.

TR-143 itself warns that concurrent tests skew each other. Admission
control bounds the damage, but scheduling at the controller (spreading
tests over time and over instances) is what actually prevents it.

## Single instance

```
diagd serve -tr471-bandwidth 5000 -max-transfers 64 \
    -allow 100.64.0.0/10,192.0.2.0/24 \
    -instance pop1-a -log-format json
```

This serves all three protocols with a 5 Gbps TR-471 budget per direction.
Suitable for a lab, a small POP, or the first tier of a rollout.

## Multiple instances per host

Above roughly 10 Gbps per NIC, run several instances per host instead of
one large one, each pinned to the NUMA node owning its interface and each
with its own control port or IP alias plus a bandwidth budget. This is the
layout the OB-UDPST best practices document recommends, expressed here as
systemd template instances (see `deploy/diagd@.service`):

```
# /etc/diagd/pop1-a.env
DIAGD_ARGS=-tr471 10.0.1.1:24601 -tr471-bandwidth 5000 -http 10.0.1.1:8080 \
    -echo 10.0.1.1:9000 -ops 127.0.0.1:9143 -instance pop1-a -log-format json
CPU_AFFINITY=0-13,28-41

# /etc/diagd/pop1-b.env
DIAGD_ARGS=-tr471 10.0.1.2:24601 -tr471-bandwidth 5000 -http 10.0.1.2:8080 \
    -echo 10.0.1.2:9000 -ops 127.0.0.1:9144 -instance pop1-b -log-format json
CPU_AFFINITY=14-27,42-55
```

Per-instance addresses (rather than ports alone) keep client configuration
uniform and let each instance be steered independently.

Host tuning that matters at high rates: raise `net.core.rmem_max` and
`net.core.wmem_max` (the reference practice is at least 2 MB), size NIC
rings generously, and keep irqbalance from fighting your affinity layout.

## Regional fleet with server lists (recommended)

The most robust scale-out mechanism is the one built into the protocols:
give every CPE a list of instances instead of one address.

- TR-471: udpst clients accept multiple servers and open one flow per
  instance (`udpst -d -B 1000 s1 s2 s3`), aggregating the result. A
  minimum connection count (`-C`) lets a test succeed with an instance
  down. Multiple flows also spread load across ECMP and LAG paths in your
  network, avoiding single elephant flows.
- TR-143: ServerSelectionDiagnostics takes a provisioned host list, pings
  all, and reports the fastest, which the controller then uses as the
  target for throughput tests.

The controller provisions region-appropriate lists. Failure handling is
per-test and automatic, no shared state and no routing tricks required.
This is the design to prefer inside a DC or region.

## DNS steering

Provision URLs and server lists as names (`diagd.pop1.example.net`) and let
a DNS controller return healthy instances, using `/healthz` as its input.
Keep TTLs short but honest (30 to 300 seconds); a CPE resolves at test
time, so steering takes effect at the next test, not mid-test. DNS steering
composes with server lists: a name per instance group, several names per
region.

## BGP anycast

Anycast gives every region one well-known address, with routing delivering
clients to the nearest healthy site. Its fit differs per protocol:

- UDP Echo Plus: excellent. Each packet is independent, so echo works even
  if routing shifts between packets (counters then reflect more than one
  responder, which loss math tolerates within a test only if shifts are
  rare; in practice per-prefix routing is stable at test timescales).
- TR-143 HTTP: good. Each TCP connection is an independent, short-lived
  test against whichever site accepted it. A mid-connection route shift
  breaks that TCP connection and fails that test attempt; this is rare and
  self-reported to the controller.
- TR-471: constrained. The test moves from the control port to a second
  UDP port, so per-flow ECMP inside a site can hash setup and test flows
  to different instances, which breaks the test. Anycast TR-471 therefore
  requires one of:
  - one instance per anycast site (site selection by BGP, no intra-site
    ECMP across instances), or
  - source-address-only hashing on the ECMP stage in front of multiple
    instances, so both flows of a client land on the same instance, or
  - not using anycast for TR-471 and steering it with server lists or DNS
    instead, keeping anycast for echo and HTTP.

Withdraw routes on failure, not on load: announce the anycast prefix while
`/healthz` answers, withdraw when it stops. Use admission control for load,
so a busy site refuses cleanly rather than disappearing for everyone. See
`deploy/bird-anycast.conf` for a bird2 example with a health-driven static
route.

## Observability pipeline

- Scrape `/metrics` per instance. `diagd_tr471_allocated_mbps` against the
  configured budget is the utilization signal; `diagd_*_rejects_total`
  rising means the fleet is undersized or tests are unscheduled.
- Ship stderr JSON logs. Each test emits one `test complete` or
  `test rejected` event carrying `instance`, `peer`, and `ref`. Aggregate
  centrally and join with controller records via `ref` (HTTP) or client
  address and time (TR-471).

## Recipes

- `deploy/diagd.service`: hardened single-instance systemd unit.
- `deploy/diagd@.service`: template unit for multi-instance hosts, with
  per-instance environment files and CPU affinity.
- `deploy/Dockerfile`: minimal container image. Use host networking; the
  TR-471 test connections use dynamically bound UDP ports, and NAT between
  subscribers and the server distorts what the tests measure.
- `deploy/bird-anycast.conf`: anycast announcement driven by `/healthz`.
