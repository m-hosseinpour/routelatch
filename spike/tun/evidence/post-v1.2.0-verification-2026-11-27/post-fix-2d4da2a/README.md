# Post-fix R-28 off-link evidence (`2d4da2a`)

Date: 2026-11-27. Product revision: `2d4da2a`
(`fix(control): record only exit_unreachable as the egress warning`). Binary
SHA-256: `1caac4666d37bbe0271e648a9105abb6b956669599ab9572f4fccca41ccb9e0c`.

This directory is evidence for the addendum in
[`../../POST-RELEASE-VERIFICATION.md`](../../POST-RELEASE-VERIFICATION.md). It is
verification only: no product or docs change other than that addendum.

## Topology

- Client A (`172.31.78.10`) in a privileged systemd container; `lo`/`eth0` only.
- Router peer R (`172.31.78.21` on A's segment, `172.31.79.2` on an internal
  `172.31.79.0/24` bridge). R owns the off-link SOCKS exit
  `172.31.79.20:11080` and the synthetic source `203.0.113.7/8:7110`.
- A has no `172.31.79.0/24` route; the exit is reached via the default gateway.
  After the break the engine's eth0-bound dial falls back to `dev eth0` and the
  peer's `arp_ignore=1` leaves the exit unreachable.

## Result

Off-link harness: **36 PASS, 0 FAIL, 0 NOTRUN** (`results.txt`). The fixed
warning appears in `status --json` `data.warnings` and in the human `status`
stderr immediately after the failed approved dial (first sample 16:33:15.778Z),
is present at ~30 s, gone at 62.1 s, and clears on the first successful approved
dial. `tun status` stays coverage-only. `pool list` is healthy at the instant of
the warning; automatic diagnostics later cool the still-unreachable exit for
60 s, and the pool returns to healthy after the cooldown and route restore.
Approved traffic stays fail-closed with no direct accept, no exit CONNECT and no
loop; unapproved traffic keeps its allowed stale direct path; recovery after
restore plus `tun off/on` is 429 ms (pool) / 215 ms (direct).

## Files

- `results.txt` — one line per check.
- `steps.txt` — route/fixture/CLI step log.
- `samples.jsonl` — status/tun/pool samples (JSON + text stdout/stderr).
- `warning-observation.txt` — focused extract for the warning, TTL and clear.
- `status-<tag>.json` — full status data, tun text, status text and pool for a
  sample.
- `isolation.txt` — host/container isolation and the timed-out public attempts.
- `environment.txt` — revision, image, kernel, toolchain and binary stamp.
- `topology.txt`, `cleanup.txt`, `host-cleanup.txt` — topology and teardown.
- `harness/r28_offlink_fix.py` — evidence-only harness (reuses
  `spike/tun/h/app_source.py`, `socks_exit.py`, `source_server.py`).

## Reproduction shape

Build `2d4da2a` inside A with `scripts/build.sh` (`ARCHES=amd64`), create the
network/containers above, then:

```sh
python3 harness/r28_offlink_fix.py \
  --a <client-container> --r <router-peer-container> \
  --binary /work/dist/routelatch-linux-amd64 --data /home/rl/data \
  --evidence <this-directory>
```

Synthetic fixtures only; no public exit, resolver, subscription or user pool.
