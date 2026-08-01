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

## What it deliberately does not do

- Store test results. Results are measured on the CPE and collected by the
  ACS or USP controller; diagd emits per-test events for the operator's
  log pipeline and holds no state once a test ends.
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
