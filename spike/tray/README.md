# Tray spikes (R13-A selection, R15-A menu capabilities)

This directory is evidence-only spike code for choosing the tray implementation of
the future `routelatch run` app mode (D26). It is a **separate Go module** and is not
linked into the product; nothing here is imported from `cmd/` or `internal/`.

- `cmd/systray-spike` — prototype on `github.com/gogpu/systray v0.3.0`
- `cmd/tray-spike` — prototype on `deedles.dev/tray v0.2.0`
- `cmd/supervisor` — minimal parent that starts one child and detects its exit
- `cmd/lifecycle-child` — D-Bus-free child used to measure parent-death handling
- `cmd/fake-watcher` — isolated-bus watcher stand-in for the "watcher but no host" case
- `cmd/menu-spike` — R15-A menu prototype with a private `/Control` driver interface
- `internal/spike` — shared watcher/host probe, notification, icon, pidfd watchdog
- `scripts/` — reproducible measurements (KDE session, absent watcher/host, lifecycle,
  R15-A offline menu harness and live KDE menu evidence)
- `evidence/` — captured outputs of the dated runs (`menu-*-2026-11-25.*` for R15-A)
- [`FINDINGS.md`](FINDINGS.md) — R13-A measurements, comparison, pin decision, limitations
- [`MENU-FINDINGS.md`](MENU-FINDINGS.md) — R15-A menu capability verdicts, host timing,
  the R15-D1 live-refresh decision, and limits

## Build and run

```sh
export PATH=/tmp/opencode/go/bin:$PATH   # go1.27.4, downloaded for this spike
export GOTOOLCHAIN=local

CGO_ENABLED=0 go build ./...
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o /tmp/tray/systray-spike \
    ./cmd/systray-spike
```

`scripts/kde-evidence.sh` needs a live KDE session and writes exactly one tray item
with the unique id `routelatch-tray-spike`; it removes the item and sends only
spike-labelled notifications. The other scripts need no desktop:

```sh
dbus-run-session -- ./scripts/host-detection.sh     # no watcher / no host / normal
dbus-run-session -- ./scripts/parent-death.sh      # SIGKILL supervisor, child exit
./scripts/menu-offline.sh /tmp/opencode/menu-build-final  # R15-A menu, isolated bus
```

`scripts/kde-menu-evidence.sh <menu-spike-binary>` is the R15-A live KDE run; it
needs `DBUS_SESSION_BUS_ADDRESS` and `DISPLAY`, creates at most one temporary magenta
spike item, and removes it at exit. See [`MENU-FINDINGS.md`](MENU-FINDINGS.md).

The spike does not install or enable services, does not change desktop settings, and
does not touch existing tray items.
