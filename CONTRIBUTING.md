# Contributing to diagd

Contributions are welcome. The most useful ones right now:

- Interoperability reports: results of running CPE TR-143 or TR-471 clients
  (OB-UDPST, RDK-B, prpl, vendor firmware) against diagd, working or not.
- Bug fixes with a reproducing test.
- Protocol correctness issues, with a reference to the relevant TR-143/TR-471
  section or OB-UDPST behavior.

Open an issue before starting large features so the design can be agreed first.

## Build and test

Go 1.25 or newer is required.

```
go build ./...
go test ./...
```

`go vet ./...` and `gofmt` must be clean; CI enforces both.

## Commits and pull requests

- Commit messages: `area: imperative summary` (for example
  `tr471: reject setup requests above bandwidth budget`).
- Branch from `main`, open a pull request against `main`. PRs are
  squash-merged, so keep one logical change per PR.
- New behavior needs a test. Protocol behavior needs a test that a peer
  implementation would notice failing.
