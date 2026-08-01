# Changelog

diagd follows semantic versioning.

## Unreleased

## 0.1.0

Initial release.

- TR-143 DownloadDiagnostics and UploadDiagnostics over HTTP with generated
  payloads, size and time-based modes, and admission control.
- TR-143 UDP Echo Plus responder with kernel receive timestamps and
  overload accounting.
- TR-471 server compatible with OB-UDPST control protocol version 20
  (accepting 11 to 19): both directions, rate search algorithms B and C,
  HMAC-SHA-256 authentication, per-direction bandwidth admission control.
- Interoperability suite running the Broadband Forum reference client in CI.
- Prometheus metrics, health endpoint, and structured JSON test events with
  URL-embedded ref correlation.
- Deployment recipes: systemd units, container image, BGP anycast example.
