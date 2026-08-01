# Architecture

diagd is the network side of CPE-initiated broadband diagnostics. It
implements the server roles of two Broadband Forum specifications in one
binary:

- TR-143: throughput tests over HTTP (download and upload) and latency tests
  over UDP Echo Plus.
- TR-471: maximum IP-layer capacity measurement, wire compatible with
  OB-UDPST control protocol version 20 (versions 11 to 19 are also accepted),
  so udpst clients embedded in existing CPE firmware work unmodified.

## Who does what

A diagnostic involves three parties:

1. The ACS (TR-069) or USP controller decides that a device should run a
   test. It writes the test parameters into the device data model, including
   the URL or address of the diagd instance to test against, and triggers the
   test.
2. The CPE executes the test and takes every measurement that matters:
   connect times, begin and end of transfer, throughput, loss, capacity. It
   stores the results in its data model and notifies the controller.
3. diagd answers the protocol correctly and fast. It generates download
   payloads on the fly, discards uploads, reflects echo packets with
   microsecond timestamps, and runs the TR-471 rate search against the
   client.

The consequence is worth stating plainly: the authoritative test result never
lives on the server. The controller collects results from the CPE over the
management protocol it already operates. diagd is not a results database and
does not try to be one.

## Test records and correlation

Although results live on the CPE, operators want to know what the server saw:
which instance served a test, how many bytes moved, whether admission control
refused it. diagd emits one structured log event per completed or rejected
test. With `-log-format json` these are machine-readable records containing:

- `instance`: the identifier passed with `-instance`, or the hostname.
- `test`: `http_download`, `http_download_timed`, `http_upload`, or `tr471`.
- `peer`: client address and port.
- `ref`: the correlation token, for HTTP tests.
- byte counts, duration, and for TR-471 the direction, maximum rate,
  datagram and loss counts.

The `ref` token is the correlation mechanism between controller and server
records. The controller owns the URL it provisions, so it can embed a
reference there:

```
Device.IP.Diagnostics.DownloadDiagnostics.DownloadURL =
    "http://diagd.pop1.example.net/1GB.bin?ref=job-9f3c2a"
```

diagd ignores unknown query parameters for test purposes and logs `ref`
verbatim. The token survives NAT, concurrent tests, and any form of load
balancing, because it travels inside the test itself. TR-471 has no
operator-settable token in its wire protocol; correlate udpst tests by client
address, port, and time window, which the controller knows because it
triggered the test.

Records flow outward, they are not held for querying. Ship the JSON events
with the log pipeline you already run and aggregate them centrally. This
keeps instances stateless, which is what makes the scaling model in
[deployment.md](deployment.md) work: an instance holds no state worth
migrating, so instances are disposable and interchangeable.

## Statelessness

Per-test state exists only while a test runs: the TR-471 session's sequence
accounting and rate index, or an HTTP transfer's progress. When the test
ends, everything is discarded. There is no test history, no session store,
and nothing to replicate between instances. If an instance dies mid-test,
the CPE reports a failed diagnostic to its controller and the controller
retries, exactly as it would for any network failure.

The UDP Echo Plus responder is the one deliberate exception, because the
specification requires it: its response counter, failure counter, and
timestamp clock run from the moment the listener starts and reset only on
restart.

## TR-143 server behavior

Downloads are generated, never read from disk. The URL selects the shape:

- `/<bytes>`, `/500KB.bin`, `/1GB` for a fixed size (decimal units).
- `/dntimebasedmode_<seconds>.txt` or any path with `?time=<seconds>` for
  time-based mode, where the server streams for at least the requested
  duration and tolerates the client resetting the connection when its own
  timer expires.

Payload content is incompressible so that compressing middleboxes cannot
inflate measured throughput. After the last byte of a sized download the
server closes the connection, which is the signal the client's end-of-test
timestamp waits for. Uploads of any length are read and discarded, with the
200 response sent only after the full body arrived.

The specification defines these tests over plain HTTP without TLS or
authentication, and diagd follows it because deployed CPE clients do.
Access control is therefore a network concern: use `-allow` CIDRs and place
instances where only subscribers can reach them.

The UDP Echo Plus responder implements the 24-byte format with the legacy
20-byte variant, keeps its counters per the specification's enable
semantics, and uses kernel receive timestamps on Linux so that echo timing
does not include scheduling delay. Kernel receive-queue overflow is counted
into TestRespReplyFailureCount, so server overload is reported to the client
as such instead of being misread as network loss.

## TR-471 server behavior

diagd speaks the OB-UDPST wire protocol: setup on the control port (24601 by
default), a per-test connection on a second port, test activation with
server-side parameter policing, then load streaming with status feedback
every trial interval. The server implements both directions (it sends the
load downstream and receives it upstream), both rate search algorithms
(type B and type C), and the sending rate table byte-identical to the
reference implementation, which both ends must compute independently.

Authentication is the protocol's HMAC-SHA-256 scheme with per-session keys
derived through the same KDF as the reference implementation. Enabling a key
(`-tr471-key`) requires every setup to be authenticated within a five second
clock window.

Admission control is the protocol's bandwidth management: with
`-tr471-bandwidth` set, clients must state their required bandwidth, and
the server rejects tests that would oversubscribe the per-direction budget.
This is the mechanism that protects result quality when many CPEs share a
server, and it is the load signal exposed on the ops endpoints for steering
decisions.

## Operational surface

A separate ops listener (default `:9143`) serves:

- `/metrics`: Prometheus text format. Transfer and test counters, active
  session gauges, allocated TR-471 bandwidth per direction.
- `/healthz`: JSON instance health with current load, for anycast route
  health checks and DNS steering controllers.

The ops listener is for operators, not subscribers. Bind it to a management
address or firewall it accordingly.
