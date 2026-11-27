# R16-H — A17 TUN acceptance (historical, e4859db)

> Historical snapshot. This is the initial R16-H acceptance run on `e4859db`.
> Its start-limit finding was fixed in `7ff128c`; the current verdict and final
> run are in [`H-ACCEPTANCE.md`](H-ACCEPTANCE.md).

**Verdict: Ready with limits.** The first-phase TUN contract worked end to end
in a disposable privileged container: 117/117 scenario checks passed, the two
gated TUN integration tests passed, and the clean-source offline suite passed on
a rerun. One operational finding (unit start-rate limit recovery) and the
documented environment limits below keep this from an unqualified `Ready`.

## Scope and revision

- Acceptance revision: `e4859db` (`main`). The primary checkout acquired
  concurrent uncommitted product/doc work during this session; it was left
  untouched and the run used a detached clean worktree at `e4859db`
  (`<tmp>/rl-h-clean`, removed afterwards). The harness and evidence
  were added only under `spike/tun/**`.
- Environment: Ubuntu 26.04 container, kernel `7.0.0-44`, systemd `259.9`,
  iproute2 `6.19.0`, Docker `29.10.1`, image `rl-tun-spike:base`
  (`sha256:cbf0ea96…`), Go `1.27.4`, product build `h-accept/e4859db`.
- No host DNS/firewall/route/interface/systemd/desktop change, no public exit,
  no private credential. Every TUN/route/unit existed only inside the two
  disposable containers on a dedicated bridge network.

Evidence: [`evidence/h-tun-2026-11-27/`](evidence/h-tun-2026-11-27/).
Reproduce with `RL_H_RUN_ID=<id> RL_H_TOOLCHAIN=$PWD/.toolchain bash spike/tun/h/run-h.sh`
from a clean `e4859db` worktree.

## Results

| Area | Result | Evidence |
| --- | --- | --- |
| Product build in container (`scripts/build.sh`, ARCHES=amd64) | PASS | `01-build-gated.txt` |
| Gated `TestTunIntegration`, `TestTunEngineIntegration` | PASS, 1.785 s | `01-build-gated.txt` |
| `tun setup` layout, root ownership, `0600` socket, idempotency, root refusal | PASS | `02-setup.txt` |
| Policy import, approved domain/IP rules, targeted mode | PASS | `03-policy.txt` |
| `tun on`: dual-stack link/halves, lease, non-root `CAP_NET_ADMIN` | PASS | `04-enable.txt` |
| App TLS/HTTP/IP through TUN; SNI/Host sniff to SOCKS; unapproved direct | PASS | `06-targeted-flows.txt` |
| all-proxy / all-direct / stopped; D13 sweep of a tracked live flow | PASS (held flow closed in 1.5 s) | `07-modes.txt` |
| IPv6 TLS/echo, approved pool vs unapproved direct | PASS | `08-ipv6.txt` |
| DNS/53 direct (UDP+TCP, v4/v6), non-53 UDP/QUIC blocked, DNS unchanged | PASS, 0 peer datagrams | `09-dns-udp.txt` |
| Engine SIGKILL: link/routes stay, EOF after 1204 ms, no direct fallback; restart adopts | PASS | `10-engine-failclosed.txt` |
| TUN service restart/lease: link absent→present gap 75 ms, flow recovers | PASS | `11-service-restart.txt` |
| TUN service SIGKILL: fail-open measured 0.40–1.27 s, auto-restart, flow recovers | PASS | `12-sigkill-failopen.txt` |
| `tun off` / Quit: link+routes removed, desire cleared, no orphan | PASS | `13-off-quit.txt` |
| Persistence: saved desire auto-enables after restart (after start-limit reset) | PASS | `14-persistence.txt`, `14a-start-limit.txt` |
| R-28 simulated default-interface change | PASS with the limit below | `15-r28-network-change.txt` |
| D21 canary (`canary2-h.invalid`, `203.0.113.8`) in files/log/state/status/journal | PASS, zero hits | `16-d21-canary.txt` |
| `tun uninstall` removes owned files, preserves user data; helper-missing warning; proxy stays up | PASS | `17-uninstall-helper.txt` |
| Final no-orphan and host checks | PASS | `18-final-cleanup.txt`, `16-host-cleanup.txt` |
| Offline `go build ./...`, `go vet ./...`, `go test -count=1 ./...` | PASS (test rerun; first run hit the known app teardown flake) | `00-host-checks.txt` |

The one observed `go test` failure was
`TestSessionBusHandshakeHonoursDeadline/tray_probe` (`read: bad file descriptor`),
the pre-existing app lifecycle flake already recorded in R16-F; the immediate
package and full-suite reruns passed. TUN tests themselves were green.

## Findings and limits

1. **Operational finding — unit start-limit recovery.** The shipped service
   has `StartLimitIntervalSec=30`, `StartLimitBurst=5`. A quick sequence of
   initial enable, manual service restart, SIGKILL restart, toggle and Quit
   (five starts in about 30 s) reaches `start-limit-hit`. Both
   `routelatch-tun.socket` and `routelatch-tun.service` then report `failed`;
   the engine honestly reports `helper=missing`, `state=unavailable` and keeps
   the normal proxy usable, but `tun on`/auto-enable cannot recover until the
   failed units are cleared. Recovery in this run required
   `systemctl reset-failed routelatch-tun.socket routelatch-tun.service` and
   `systemctl start routelatch-tun.socket`. Persistence passed after that.
   This is not a silent bypass/leak, but it is a recovery/documentation gap
   worth a follow-up (automatic reset on an explicit enable, or documented
   recovery). Evidence: `14a-start-limit.txt`, `14-persistence.txt`.
2. **R-28 test topology.** Before `tun off/on` the engine kept the old physical
   interface (`tun.bypass interface=eth0`), so the direct probe stayed stale via
   the old route; after `tun off/on` it rebound to `dummy0`, and the direct probe
   failed closed. The approved pool probe remained reachable because the test
   exit fixture is on-link on `eth0`; it never turned into a direct destination
   connection. A real off-link pool under an interface change was not tested
   (no public exit). Evidence: `15-r28-network-change.txt`.
3. **Helper-missing warning vocabulary.** With the helper really absent, the
   engine reports `helper=missing` plus `tun_service_unavailable`, not
   `tun_helper_missing`; this is exactly what `contracts.md §10.2` specifies
   when the runtime status call fails. The proxy stays available.
4. **D21 level guarantee.** Shipped service config keeps `logLevel:"none"` and
   `access:"none"`; the canary was absent from all product surfaces. The
   literal “no destination even at Xray debug level” limitation recorded in
   `spike/tun/FINDINGS.md`/§10.5 is unchanged.
5. **Not run / outside this acceptance:** real KDE/Wayland tray icon and menu
   (headless container; R16-F code tests only), public/off-link destinations,
   DNS-through-pool and UDP forwarding (D30/R16-I/J), ECH, per-app/per-UID,
   Windows/macOS/Android, and a second full-tunnel VPN. Package/artifact checks
   were not rebuilt in H; G covers them with an isolated `DIST` (the existing
   local `dist/packages` is stale and unrelated).

## Cleanup

Containers, network and test TUN/routes were removed; the host has no
`rl-tun0`, no `198.18/15` or `fc00::/64` route, and no `rl-h-*` container or
network. No product code, docs, plan or packaging was edited, nothing was
staged or committed, and the primary checkout's concurrent work was preserved.
