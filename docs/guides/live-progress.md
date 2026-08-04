# Live progress

TR-143 clients report once, at the end. A CPE runs the whole test, then
files `TestBytesReceived`, `BOMTime` and `EOMTime` in its next session.
An operator watching a support screen sees a spinner for the duration and
a number at the end.

diagd is the other party to that same TCP stream. It wrote every
generated block and read every uploaded chunk, so it can report the
transfer while the transfer is happening. `GET /live` on the operational
listener returns the tests currently on the wire.

This is what lets an ACS or USP controller show a moving figure during a
test without inventing one.

![The ACS sets the test URL with a ref tag on the CPE, the CPE transfers
against diagd, and the ACS polls diagd's live endpoint for that same ref
once a second while the CPE's own figures arrive only at
completion](../assets/images/live-progress-integration.png)

## Correlating a test to a run

Attribution is transactional, not per device. A peer address is who the
packets came from, which NAT makes ambiguous, and a subscriber identifier
is not something diagd knows. Instead the caller mints a tag per test and
puts it in the test URL as `ref`:

```
http://diagd.example.net:8080/dntimebasedmode_8.txt?ref=e3c1a9f2-...
```

diagd carries that tag on the in-flight record and on the completion
event, so the caller filters `/live` down to exactly the test it started:

```
GET http://diagd.example.net:9143/live?ref=e3c1a9f2-...
```

Any opaque string works. Use whatever identifier your platform already
has for the operation, so the live samples, the completion event and your
own run record all carry the same key.

## The sequence

For an ACS driving TR-143 over CWMP, with its own run identifier `R`:

1. Set `DownloadDiagnostics.DownloadURL` to the diagd test URL with
   `?ref=R`, and `DiagnosticsState` to `Requested`, in the same
   SetParameterValues. The CPE starts the test after the session ends.
2. Poll `GET /live?ref=R` on the operational port, once a second. Each
   response carries `bytes` and `elapsed_ms` for the transfer. Bytes over
   elapsed is the average rate so far.
3. Poll `DownloadDiagnostics.DiagnosticsState` on the CPE at whatever
   cadence your platform allows, until it leaves `Requested`. This is the
   completion signal. TR-143 says a CPE should send an Inform with
   `8 DIAGNOSTICS COMPLETE`, but not all firmware does, so treat the
   event as an optimisation and the poll as the contract.
4. Read the CPE's own result parameters. Those are the authoritative
   figures: they include the client side of the transfer, which diagd
   cannot see.

The same shape applies to a USP controller, and to uploads with
`UploadDiagnostics` and `http_upload`.

## Response

```json
{
  "transfers": [
    {
      "test": "http_download_timed",
      "ref": "e3c1a9f2-...",
      "peer": "203.0.113.24:41582",
      "bytes": 310378496,
      "elapsed_ms": 3219,
      "done": false
    }
  ]
}
```

`test` is one of `http_download`, `http_download_timed` or
`http_upload`. `elapsed_ms` is measured from the first byte diagd moved,
not from when the CPE was told to start, so it excludes the CPE's setup
and connection time.

Filters, both optional and combinable:

- `?ref=` returns the transfers carrying that tag. This is the one to
  build on.
- `?peer=` returns the transfers from that source address. Useful when
  debugging by hand, ambiguous behind NAT.

## Finished transfers stay briefly

A transfer remains in `/live` for 15 seconds after it ends, flagged
`done: true`, with `elapsed_ms` frozen at completion so the reported
average stays the transfer's own.

Without that window, a caller polling every second races the end of the
transfer: one poll shows real progress, the next shows an empty list, and
the final counts are gone. With it, a caller that polls at any cadence
under 15 seconds always sees a terminal sample for a test it was
watching.

That sample is also useful when firmware reports poorly. Some CPEs run a
time-based test correctly and then file an empty result, with zero byte
counts and blank timestamps. diagd measured the same transfer, so a
caller can fall back to the server side figure rather than showing
nothing. If you do that, record which side produced the number, because
the two are not measuring quite the same thing.

## What the numbers are, and are not

- **An average, not an instantaneous rate.** Bytes over elapsed since the
  transfer started. It converges as the test runs and is smooth by
  nature; it will not show a momentary dip the way a per-interval
  calculation would. If you want per-interval rates, difference two
  successive samples yourself.
- **The server's view.** It counts bytes diagd moved into or out of the
  socket. It does not include the client's own accounting, and for a
  download it cannot see what the CPE actually received after loss and
  retransmission. The CPE's TR-143 result remains authoritative.
- **Not a stored result.** diagd holds nothing after the grace window.
  Persisting the measurement is the caller's job.

## Operational notes

- `/live` is on the operational listener (`-ops`, default `:9143`), not
  the test listener. Keep that port on a management network; it exposes
  peer addresses and any tags you put in test URLs, so do not put
  subscriber-identifying information in `ref`.
- One second is a sensible poll interval for a user-facing figure. The
  handler serialises a small slice under one mutex, so the cost is
  negligible, but there is nothing to gain below the rate at which a
  human reads a number.
- Some firmware strips or rewrites query parameters on `DownloadURL`. If
  a test arrives without your tag, `/live` still lists it; a caller can
  fall back to matching the single in-flight transfer of the expected
  kind when it knows only one test is running.
- Timed tests suit live display better than sized ones. A 10 MB file on a
  fast line is finished in a fraction of a second, which measures a burst
  and leaves nothing to watch; a duration measures a sustained rate and
  behaves the same on every line speed.
