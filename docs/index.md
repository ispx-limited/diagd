---
hide:
  - toc
---

<section class="ispx-home" markdown>
<div class="ispx-home__copy" markdown>

<h1 class="ispx-sr-only">diagd</h1>

<p class="ispx-kicker">A network test server for CPE broadband diagnostics. Point TR-143 and TR-471 tests from subscriber devices at it, out of the box.</p>

<div class="ispx-actions" markdown>
[Get started](guides/quickstart.md){ .ispx-btn .ispx-btn--primary }
[View on GitHub](https://github.com/ispx-limited/diagd){ .ispx-btn .ispx-btn--ghost }
</div>

<div class="ispx-signal__chips">
  <span class="ispx-chip">TR-143 HTTP and UDP Echo Plus</span>
  <span class="ispx-chip">TR-471 via OB-UDPST protocol</span>
  <span class="ispx-chip">Single static binary</span>
</div>

</div>
</section>

## Built for running diagnostics at ISP scale

<div class="ispx-card-grid" markdown>

<div class="ispx-card" markdown>
### Works with deployed CPE firmware
Wire compatible with OB-UDPST control protocol version 20, the TR-471 client embedded in RDK-B and prpl device stacks. CI runs the Broadband Forum reference client against every change.
</div>

<div class="ispx-card" markdown>
### Scales out, not up
Stateless instances with bandwidth admission control, health endpoints for anycast and DNS steering, and reference designs from a single box to a regional fleet.
</div>

<div class="ispx-card" markdown>
### Observable by default
Prometheus metrics per instance and one structured JSON event per test, carrying the correlation token your ACS embeds in the test URL.
</div>

</div>
