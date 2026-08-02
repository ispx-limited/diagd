# Quickstart

## Try it

Download the latest release (Linux, amd64 or arm64, statically linked) and
run it in the foreground:

```
curl -LO https://github.com/ispx-limited/diagd/releases/latest/download/diagd_linux_amd64.tar.gz
tar xzf diagd_linux_amd64.tar.gz
./diagd_linux_amd64/diagd serve
```

Or build from source, with Go 1.25 or newer:

```
git clone https://github.com/ispx-limited/diagd.git
cd diagd
go build ./cmd/diagd
./diagd serve
```

Linux is the supported platform; other unixes build and run with reduced
timestamp accuracy on the echo responder.

## Install as a service

The release includes an installer that puts the binary in
`/usr/local/bin` and, when the machine runs systemd, installs the
hardened service unit:

```
sudo ./diagd_linux_amd64/install.sh
sudo systemctl enable --now diagd
```

Edit the unit (`systemctl edit diagd`) to set your flags: instance name,
bandwidth budget, allow list. On upgrades the installer never overwrites
an edited unit; it writes the new version alongside as
`diagd.service.new` and tells you to compare.

Without systemd, the installer installs the binary only. diagd is a
plain foreground process that logs to stderr and stops cleanly on
SIGTERM, so any supervisor works: OpenRC, runit, s6, supervisord, or a
container runtime.

The daemon logs its listeners on startup: TR-143 HTTP on `:8080`, UDP Echo
Plus on `:9000`, TR-471 control on `:24601`, and the operational endpoints
on `:9143`.

## Prove it works

A TR-143 style download of 100 MB of generated data:

```
curl -o /dev/null -w "%{size_download} bytes in %{time_total}s\n" \
    http://localhost:8080/100MB.bin
```

A TR-471 capacity test with the Broadband Forum reference client
([OB-UDPST](https://github.com/BroadbandForum/obudpst)):

```
udpst -d localhost
```

The client prints per-second sub-interval rates and a summary with the
maximum IP-layer capacity. `udpst -u localhost` runs the upstream
direction.

Instance health and metrics:

```
curl http://localhost:9143/healthz
curl http://localhost:9143/metrics
```

## First real deployment

Give the daemon an identity, a bandwidth budget, and structured logs:

```
./diagd serve -instance pop1-a -tr471-bandwidth 5000 \
    -allow 100.64.0.0/10 -log-format json
```

Then read [Architecture](../architecture.md) for how the pieces fit and
[Deployment](../deployment.md) for firewalling, sizing, and the reference
designs for scaling out.
