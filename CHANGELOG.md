# Changelog

## [0.2.0](https://github.com/ispx-limited/diagd/compare/v0.1.0...v0.2.0) (2026-08-21)


### Added

* **ops:** /live reports in-flight transfers as measured server-side ([#6](https://github.com/ispx-limited/diagd/issues/6)) ([385b4a4](https://github.com/ispx-limited/diagd/commit/385b4a4ea94a5de598ed795a77bae05bc5d28ca8))
* **ops:** grace window and ref/peer filters on /live ([#7](https://github.com/ispx-limited/diagd/issues/7)) ([8addd63](https://github.com/ispx-limited/diagd/commit/8addd6376caa192671cb54f5e5a92b82efeed69c))
* **release:** the changelog is generated and published on the docs site ([#10](https://github.com/ispx-limited/diagd/issues/10)) ([1e822dc](https://github.com/ispx-limited/diagd/commit/1e822dc22176e45c53da3068a4e0dafefaaeece0))

## 0.1.0 (2026-08-02)

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
