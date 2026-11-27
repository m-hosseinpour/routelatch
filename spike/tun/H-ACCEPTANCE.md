# R16-H — A17 TUN acceptance

**Verdict: Ready with limits.** The final rerun at `7ff128c` passed all 158
scenario checks (failed 0, not-run 0), the targeted addendum passed 12 checks
with 2 marked not-run, both gated TUN integration tests, the package checks, and
a clean `go build`/`go vet`/`go test -count=1 ./...` run. The repaired unit
start limit and the new peer-ownership, socket/link-witness, and two-engine
paths passed. Only the documented environment and first-phase scope limits
below remain.

## Scope and revision

- Acceptance revision: `7ff128c` (`main`), built as `h-accept` with commit
  `7ff128c`. The product, docs and plan were clean; only the acceptance harness
  and evidence under `spike/tun/**` were untracked.
- Environment: Ubuntu 26.04.1 container, kernel `7.0.0-44-generic`, systemd
  `259.9`, iproute2 `6.19.0`, Docker `29.10.1`, image
  `sha256:cbf0ea96…`, Go `1.27.4`. One disposable privileged systemd container
  plus a controlled peer container on a dedicated bridge
  (`172.31.77.0/24`, `fd00:77::/64`; the addendum used the equivalent
  `172.31.78.0/24`, `fd00:78::/64`).
- No host DNS/firewall/route/interface/systemd/desktop change, no public exit,
  no private credential. Every TUN, route, unit and listener existed only inside
  the two disposable containers.
- Evidence: [`evidence/h-tun-2026-11-27-final/`](evidence/h-tun-2026-11-27-final/)
  for the main scenario and
  [`evidence/h-tun-2026-11-27-addendum/`](evidence/h-tun-2026-11-27-addendum/)
  for the targeted addendum. The initial `e4859db` run is preserved in
  [`evidence/h-tun-2026-11-27/`](evidence/h-tun-2026-11-27/) and
  [`H-ACCEPTANCE-e4859db.md`](H-ACCEPTANCE-e4859db.md).
- The `socks5://172.31.77.20:11080#exit-h` exit, `203.0.113.0/24` and
  `2001:db8::/32` destinations are synthetic local fixtures; no public exit or
  credential was used.
- Reproduce from a clean working tree:
  `RL_H_RUN_ID=<id> RL_H_EVID=$PWD/spike/tun/evidence/<new-dir> bash spike/tun/h/run-h.sh`
  for the main scenario and
  `RL_H_ADDENDUM_RUN_ID=<id> RL_H_ADDENDUM_EVID=$PWD/spike/tun/evidence/<new-dir> bash spike/tun/h/run-addendum.sh`
  for the addendum. The harness refuses to replace a non-empty evidence
  directory unless `RL_H_OVERWRITE=1` is set.

## Results

| Area | Result | Evidence |
| --- | --- | --- |
| Product build/package (`scripts/build.sh`, ARCHES=amd64, SHA256SUMS, BUILD-INFO) | PASS | `01-build-gated.txt` |
| Gated `TestTunIntegration`, `TestTunEngineIntegration` | PASS, 1.901 s | `01-build-gated.txt` |
| `tun setup`: layout, root ownership, `0600` socket, idempotency, root refusal, unit limits `300/20/none`, `BindsTo=` | PASS | `02-setup.txt` |
| Policy import, approved domain/IP rules, targeted mode | PASS | `03-policy.txt` |
| `tun on`: dual-stack link/routes, lease, non-root `CAP_NET_ADMIN` | PASS | `04-enable.txt` |
| App TLS/HTTP/IP through TUN; SNI/Host sniff to SOCKS; unapproved direct | PASS | `06-targeted-flows.txt` |
| all-proxy / all-direct / stopped; D13 sweep closed the held flow in 1.632 s | PASS | `07-modes.txt` |
| IPv6 TLS/echo, approved pool vs unapproved direct | PASS | `08-ipv6.txt` |
| DNS/53 direct: IPv4 (UDP/TCP) and IPv6 (UDP A/AAAA, TCP) to controlled resolvers, physical-interface source, DNS unchanged; non-53 UDP/QUIC blocked, 0 peer datagrams | PASS | `09-dns-udp.txt`, addendum `10-ipv6-dns.txt` |
| Engine SIGKILL: link/routes stay, EOF after 1206 ms, no direct fallback; restart adopts | PASS | `10-engine-failclosed.txt` |
| TUN service restart/lease: link gap observed, flow recovers | PASS | `11-service-restart.txt` |
| TUN service SIGKILL: fail-open 0.605–1.390 s, auto-restart, flow recovers | PASS | `12-sigkill-failopen.txt` |
| `tun off` / Quit: link+routes removed, desire cleared, no orphan | PASS | `13-off-quit.txt` |
| Persistence: saved desire auto-enables after an engine restart (Login/autostart auto-enable NOT RUN) | PASS for engine restart | `results.txt` (auto-enable assertion); `14a-start-limit.txt` (unit state) |
| Package: 4-arch build, `check-package.sh`, `check-artifact.sh`, no unit/tmpfiles/helper/postinst | PASS | addendum `03-package.txt` |
| TUN-service SIGKILL fail-open: status samples inside the link gap report no error/unavailable (NOT RUN); no clean off claimed; auto-restart PASS | NOT RUN (status visibility) | addendum `11-failopen-status.txt` |
| R-28 simulated default-interface change | PASS with the limit below | `15-r28-network-change.txt` |
| D21 canary (`canary2-h.invalid`, `203.0.113.8`) in files/log/state/status/journal | PASS, zero hits | `16-d21-canary.txt` |
| `tun uninstall` removes owned files, preserves user data; helper-missing warning; proxy stays up | PASS | `17-uninstall-helper.txt` |
| Final no-orphan and host checks | PASS | `18-final-cleanup.txt`, `16-host-cleanup.txt` |
| Offline `go build ./...`, `go vet ./...`, `go test -count=1 ./...` | PASS on the first run | `00-host-checks.txt` |

### Start-limit fix

The quick enable/manual restart/SIGKILL-restart/toggle/Quit sequence now leaves
`routelatch-tun.service` active with `Result=success` and no
`start-limit-hit`; the socket stays active and the saved desire auto-enables
without a reset (`14a-start-limit.txt`).

The bound itself was then exercised deterministically: after clearing the rate
counter, 20 consecutive `systemctl restart` calls succeeded and the 21st failed
with `Result=start-limit-hit`. At that point `rl-tun0` and both TUN routes were
absent, `tun status` reported `state=unavailable` with desired coverage still
true (not `on`, not a clean `off`), and `tun on` exited `7` with
`tun_helper_unavailable`; no bypass-clear failure was logged. The documented
recovery (`systemctl reset-failed routelatch-tun.socket
routelatch-tun.service` then `systemctl start routelatch-tun.socket`) restored
coverage and the approved pool flow (`20-start-limit.txt`).

### Peer ownership and two-engine coverage

A second headless engine was run from its own data dir with
`http/socks=127.0.0.1:8417/8418` and `tun_enabled=true`. It reported
`tun_conflict`, kept its listeners open, and left the first engine's live
`rl-tun0` and bypass intact. Its `tun on` and `tun off` both exited `6` with
`state_locked`/`tun_conflict`; the first engine's approved flow kept working,
and the peer coverage and flow survived the second engine's Quit
(`19-two-engine.txt`).

### Socket/link witness

Stopping only `routelatch-tun.socket` bound the service down and removed
`rl-tun0`. A witness `rl-tun0` (dummy link plus catch-all half routes) made
`tun status` report `unavailable`/`tun_conflict`; `tun off` did not claim `off`
and kept the engine bypass: direct app traffic into the witness failed closed
(timeout), while a SOCKS-to-pool TLS flow still reached the peer (`TLS-OK`).
After removing the witness the same missing socket produced a clean `off`;
restarting the socket and helper restored coverage and the pool flow
(`21-socket-link-witness.txt`).

### Addendum: IPv6 DNS, fail-open status, package

The targeted addendum at the same revision closed the validator gaps:

- **IPv6 DNS/53 direct (PASS).** A controlled resolver on `[2001:db8::7]:53`
  answered UDP A (`203.0.113.7`), UDP AAAA (`2001:db8::7`) and TCP A queries sent
  through `rl-tun0`. Its log records exactly two UDP datagrams and one TCP query,
  all sourced from the physical interface `fd00:78::10`; `/etc/resolv.conf` was
  unchanged. Evidence: `10-ipv6-dns.txt`, `00-peer-listeners.txt`.
- **Fail-open status visibility (NOT RUN).** Two status samples landed inside the
  measured link gap (1.215 s): one management call timed out and one returned
  `tun.state=on` with no `last_error`; no error/unavailable state was observed.
  The same probe did not claim a clean `off`, and the service auto-restarted.
  The tray consumes these fields, but a real icon was not observed in the
  headless container. The earlier acceptance did not claim icon error
  visibility, and this addendum does not either. Evidence:
  `11-failopen-status.txt`.
- **Package checks at `7ff128c` (PASS).** In an isolated `DIST`, all four
  architectures were built, `scripts/check-package.sh` and
  `scripts/check-artifact.sh` passed, and an explicit scan of tarballs, `.deb`
  payloads, control members and RPM specs found no systemd unit, tmpfiles entry,
  TUN helper, autostart entry or maintainer script. Binary RPM construction and
  inspection remain CI's job. Evidence: `03-package.txt`.
- **Synthetic fixtures.** The local SOCKS exit, `203.0.113.0/24` and
  `2001:db8::/32` are test-only; no public exit, credential or real resolver was
  used.

## Limits

1. **Environment.** Headless TUN container: no tray/icon in this run; the
   separate Plasma/X11 run in [`H-TRAY.md`](H-TRAY.md) verified the real
   helper-missing/off/error surface and a fake-status active visual, while a
   real TUN-active host icon/toggle remains untested (helper install was not
   authorized). No public/off-link destinations, no second full-tunnel VPN, and
   no Windows/macOS/Android. DNS-through-pool and UDP forwarding remain
   out of phase one (D30/R16-I/J), as do ECH and per-app/per-UID coverage.
   Persistence was tested by restarting the engine; Login/autostart auto-enable
   was **not run** because the container has no desktop session or login manager.
2. **R-28 topology.** The test pool exit is on-link on `eth0`, so the approved
   pool probe stayed reachable while the direct path followed the changed
   default or failed closed. A real off-link pool under an interface change was
   not tested; the documented stale-direct/off/on behavior was observed and no
   approved flow turned direct.
3. **Start-limit recovery is manual by design.** A crash loop still stops after
   20 starts per five minutes. During recovery the helper cannot be reached and
   status uses the contract's `helper=missing` plus `tun_service_unavailable`
   vocabulary; coverage stays off and no direct fallback is claimed. The
   documented `reset-failed` + socket start is required.
4. **D21 level guarantee.** The shipped service keeps `logLevel:"none"` and
   `access:"none"`; the canary was absent from every product surface. The
   literal “no destination even at Xray debug level” limitation in
   `spike/tun/FINDINGS.md`/§10.5 is unchanged.
5. **Package checks.** H ran `scripts/build.sh`, `scripts/package.sh`,
   `scripts/check-package.sh` and `scripts/check-artifact.sh` for all four
   architectures in an isolated `DIST`, plus an explicit no-unit/tmpfiles/helper/
   postinst scan. Binary RPM construction/inspection remains CI's job.
6. **Fail-open status visibility is not claimed.** Status/tray-facing fields
   were sampled, but no in-window sample exposed an error or `unavailable`; the
   observed sample reported `on`. Icon error/color behavior in the gap is
   therefore **not run/not verified**, not a passed claim.
7. The gated tests and the container build ran inside the disposable container;
   the offline build/vet/test suite ran on the host against `7ff128c`. The
   previous `e4859db` app-teardown flake did not recur.

## Cleanup

Containers and the dedicated network were removed. The host has no `rl-tun0`,
no `198.18/15` or `fc00::/64` route, and no `rl-h-*` or `rl-ha-*` container or
network. No product code, docs, plan or packaging was edited, nothing was staged
or committed, and the initial evidence and the concurrent untracked H work were
preserved. Machine-local temporary paths in the H reports/evidence were
replaced with `<tmp>`; the synthetic local fixtures remain labeled as such.
