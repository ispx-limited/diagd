# diagd

A network test server for CPE broadband diagnostics. diagd implements the
server side of Broadband Forum TR-143 (HTTP throughput tests and UDP Echo
Plus) and TR-471 (maximum IP-layer capacity), so ISPs can point the
diagnostics built into subscriber devices at their own infrastructure.

The TR-471 implementation is wire compatible with OB-UDPST control protocol
version 20 (versions 11 to 19 are also accepted). udpst clients already
embedded in CPE firmware run against diagd unmodified, and CI proves it by
running the Broadband Forum reference client against every change.

## What it does

- TR-143 DownloadDiagnostics and UploadDiagnostics over HTTP: generated
  payloads by size or duration, including the time-based URL conventions,
  with admission control for concurrent tests.
- TR-143 UDP Echo Plus with kernel receive timestamps and correct overload
  accounting, plus plain RFC 862 echo.
- TR-471 in both directions with rate search algorithms B and C, optional
  HMAC-SHA-256 authentication, and per-direction bandwidth admission
  control.
- Operational endpoints per instance: Prometheus metrics and a health
  endpoint for anycast or DNS steering, and one structured JSON event per
  test for central collection.

One static binary. Dependencies are limited to golang.org/x/net and
golang.org/x/sys.

## Quickstart

Download the latest release (Linux, amd64 or arm64):

```
curl -LO https://github.com/ispx-limited/diagd/releases/latest/download/diagd_linux_amd64.tar.gz
tar xzf diagd_linux_amd64.tar.gz
./diagd_linux_amd64/diagd serve
```

To install it as a service, `sudo ./diagd_linux_amd64/install.sh` puts the
binary in `/usr/local/bin` and, on systemd machines, installs the service
unit. Without systemd, run the binary under any supervisor; it is a plain
foreground process.

Or build from source with Go 1.25 or newer:

```
go build ./cmd/diagd
./diagd serve
```

Then `curl -o /dev/null http://localhost:8080/100MB.bin` for a download
test, or `udpst -d localhost` with the
[OB-UDPST](https://github.com/BroadbandForum/obudpst) client for a capacity
test. See the [documentation](https://docs.diagd.ispx.co/) for
the architecture and the deployment reference designs, from a single box
to an anycast fleet; sources are in `docs/`.

## Live progress

TR-143 clients report once, at the end, which leaves an ACS showing a
spinner for the length of a test. diagd is the other party to the same
TCP stream, so it can report the transfer while it happens:

```
GET :9143/live?ref=<your-run-id>
```

Put your own identifier in the test URL as `?ref=`, and diagd carries it
on the in-flight record and the completion event, so a caller filters
`/live` down to exactly the test it started. Each entry gives `bytes` and
`elapsed_ms`; bytes over elapsed is the average rate so far. Finished
transfers stay for 15 seconds flagged `done`, so a poller cannot race the
end of a test and lose the final counts.

The CPE's own TR-143 result stays authoritative. This is for showing
progress, and for the case where firmware runs a test correctly and then
files an empty report. See the
[live progress guide](https://docs.diagd.ispx.co/guides/live-progress/).

## What it deliberately does not do

- Store test results. Results are measured on the CPE and collected by the
  ACS or USP controller; diagd emits per-test events for the operator's
  log pipeline. In-flight transfers are held in memory for `/live` and
  dropped 15 seconds after they end, so nothing survives beyond that.
- TLS or authentication on TR-143 endpoints. The specification defines
  these tests over plain HTTP and deployed CPE clients expect that;
  restrict access with `-allow` CIDRs and network placement.
- FTP transport for TR-143 downloads and uploads.

## Current limitations

- Linux is the tested platform. Other unixes build and run with reduced
  timestamp accuracy on the echo responder.
- No bundled test client yet; validate deployments with curl and the
  OB-UDPST client.
- ECN CE counting for TR-471 rate adjustment is not implemented; the CE
  threshold parameter is accepted and echoed but never triggers a rate
  reduction.
- Interoperability is proven against OB-UDPST 9.0.0. Reports from other
  TR-143 and TR-471 client implementations are welcome, working or not.

## License

Apache-2.0. Copyright ISPX LIMITED. See `LICENSE` and `NOTICE`.
