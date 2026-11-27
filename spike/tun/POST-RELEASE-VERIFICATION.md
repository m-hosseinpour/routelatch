# Post-release verification — TUN checks after v1.2.0

**Date:** 2026-11-27
**Repository/revision:** RouteLatch `899b036` (`main`), clean except this report and
`spike/tun/evidence/post-v1.2.0-verification-2026-11-27/**`.
**Product revision:** `git diff --stat 7ff128c..HEAD -- cmd internal scripts packaging deploy go.mod go.sum`
is empty, so the TUN product code is the code accepted at `7ff128c` (A17).
**Scope:** verification only. No host network/DNS/firewall/systemd/desktop/service
change, no public exit or resolver, no private subscription/credential, no user pool.
No product, docs or plan edit. The only additions are this report and its evidence.

**Verdict: the addendum outcome is unchanged; the previously untested R-28 off-link
path is safe (fails closed, no leak, no loop, `tun off/on` rebinds and recovers) but its
status surface does not report the degraded condition. Start-limit remains the recorded
PASS.** Details and the one conformance gap are below.

## 0. Environment and isolation

Host (re-confirmed before and after the run, `isolation.txt`, `final-host-check.txt`):
default via `wlp0s20f3`; no IPv6 default; `tailscale0` present with only a link-local
address and `Tailscale is stopped`; no `rl-tun0`; no `198.18/15` or `fc00::/64` route.

One disposable privileged systemd container A (client, `172.31.78.10/24`), peer B
(`172.31.78.20`), and a privileged router peer B2 (`172.31.78.21` on A's segment and
`172.31.79.2` on a second, `--internal` bridge `172.31.79.0/24`). Image
`rl-tun-spike:base` (`sha256:cbf0ea96…`), Ubuntu 26.04.1, kernel `7.0.0-44-generic`,
systemd `259.9`, Docker `29.10.1`.

Inside A: links are only `lo` and `eth0`; no `tailscale0`, no `rl-tun0`, no host
`192.168.1.0/24` or `100.64/10` route. With TUN off, direct attempts to
`1.1.1.1:443`, `93.184.216.34:80`, `8.8.8.8:53`, `2606:4700:4700::1111:443` and
`2001:4860:4860::8888:53` all timed out. Everything below is runtime-observed against
local synthetic fixtures (`203.0.113.0/24`, `2001:db8::/32`, a local SOCKS exit).

## 1. H addendum rerun (IPv6 DNS, package, fail-open status)

Stock `spike/tun/h/run-addendum.sh` at this revision:
**SUMMARY passed=14 failed=0 notrun=2**, identical to `7ff128c`
(`h-addendum/results.txt`).

| Check | Result |
| --- | --- |
| IPv6 DNS/53 direct: UDP A, UDP AAAA, TCP A through `rl-tun0` | PASS; answers `203.0.113.7` / `2001:db8::7` |
| Resolver log: exactly 2 UDP datagrams, 1 TCP query, source `fd00:78::10` | PASS |
| `/etc/resolv.conf` unchanged | PASS |
| Package build (4 arches), `check-package.sh`, `check-artifact.sh` | PASS |
| No unit/tmpfiles/helper/autostart/maintainer script in tarballs, `.deb` control/payload or RPM spec | PASS |
| Tray icon rendering in the gap | NOT RUN (headless) |

### Fail-open status sampling (repeated SIGKILL)

The stock addendum killed the service once: a call that began just before the link
dropped timed out, and one call started inside the gap reported `on`, matching the prior
run. Because the sampler records a call's **start** time, a call that spans recovery is
attributed to the gap while reporting the recovered state. A timed rerun
(`failopen-timed/`, 4 SIGKILLs, calls record start and end) resolves this:

| Kill | link gap | `state=on` again | calls started in gap | outcome |
| --- | --- | --- | --- | --- |
| 1 | 1135 ms | 1287 ms | 1 JSON + 1 text | blocked 467 ms into recovery, `on` |
| 2 | 1215 ms | 1375 ms | 1 JSON + 1 text | blocked 569 ms into recovery, `on` |
| 3 | 1315 ms | 1458 ms | 1 JSON + 1 text | blocked 631 ms into recovery, `on` |
| 4 | 1291 ms | 1424 ms | 1 JSON + 1 text | blocked 631 ms; the text call timed out (704 ms) |

- No status call **completed inside a gap** with `on`; every in-gap call blocked until
  recovery or timed out. Post-recovery fields were always
  `state=on, desired=true, helper=installed, last_error=null, detail=null`, exit 0
  (`11-failopen-status.txt`).
- So the gap is visible as latency or a timeout to a bounded caller, not as a false
  `on`; no `unavailable`/`last_error` state was ever produced. The prior “in-gap sample
  reported `on`” is consistent with a call that started 0.55 s before the measured
  recovery and returned afterwards. **The addendum's NOT RUN verdict on in-gap error
  visibility is unchanged; only its interpretation is refined.** No defect found.

## 2. R-28 with an off-link pool exit

Topology (`r28-offlink/r28-offlink-topology.txt`): the pool exit listens on
`172.31.79.20:11080`, owned by B2 on the second bridge. A has **no** `172.31.79.0/24`
route; `ip route get 172.31.79.20 oif eth0` is
`172.31.79.20 via 172.31.78.21 dev eth0`. A's shared segment answers no ARP for it
(`arp_ignore=1` on B2's eth0), so the exit is reachable only through the default
gateway. The approved destination `203.0.113.7` and the unapproved `203.0.113.8` stay
on B2's shared interface so a direct fallback would be observable as source
`172.31.78.10`.

| Step | Observation |
| --- | --- |
| Baseline approved flow (203.0.113.7) | pool, 420–429 ms, peer `203.0.113.7`; exit log shows the CONNECT |
| Baseline unapproved flow (203.0.113.8) | direct, 215 ms, peer `172.31.78.10` |
| Replace default with `dummy0` (10.255.255.1/32) | applied; no route to `172.31.79.20`, ARP stays INCOMPLETE |
| Approved flow after the change | **fails closed**, `eof` after 3.47 s; no exit CONNECT; no accept with source `172.31.78.10` for 203.0.113.7 |
| Loop / direct leak | none: no new exit connect, no approved direct accept |
| Unapproved direct after the change | still works (215 ms) — the stale on-link path; allowed by R-28, not a leak |
| `tun status` / `status` during the break | `state=on, desired=true, helper=installed, last_error=null, detail=null`; warnings only `tun_dns_direct`, `tun_udp_blocked`; `pool list` still `healthy`, `consecutive_failures=0` |
| `tun off`/`tun on` on `dummy0` | bypass rebinds (`tun.bypass: interface=dummy0`); approved times out (4005 ms); direct times out (3004 ms); no leak |
| Restore default, `tun off/on` | approved recovers (423–432 ms, peer 203.0.113.7); direct recovers (216 ms) |

Harness `r28-offlink/results.txt`: **23 PASS, 0 FAIL, 1 NOTRUN**. The NOTRUN is the
status check.

**Conformance gap (not fixed, not safety-critical).** `docs/contracts.md` R-28 says
“`status` shows the error” and the H limit called off-link untested. Here the approved
path fails closed and nothing leaks or loops, but status stays `on` with no error, no
detail, no warning, and the pool record stays healthy even after the failed dial
(`r28-offlink/r28-status-detail.txt`). A tray would show active coverage while approved
traffic fails. This was not claimed by the `7ff128c` acceptance (it only tested the
on-link topology) and is now recorded as an open conformance gap. Whether the intended
fix is a pool-health signal or an R-28 wording change is a product decision; no code was
changed here.

## 3. Start limit

The installed unit at this revision still has `StartLimitIntervalSec=5min`,
`StartLimitBurst=20`, `StartLimitAction=none`, `BindsTo=routelatch-tun.socket`,
`Restart=on-failure` (`start-limit-confirm.txt`). The deterministic bound/recovery
(20 restarts succeed, 21st `start-limit-hit`, `reset-failed` + socket start recovers)
was recorded at `7ff128c` and is not rerun; the product is unchanged. The open-work
backlog entry “recovery دستی start-limit” is stale: it is a documented, tested
limitation, not an outstanding verification.

## Findings

1. **R-28 off-link status visibility (open conformance gap).** Safety properties hold,
   but `tun status`/`status` and `pool list` do not report the unreachable pool; the
   only observable is the failed approved connection. Contradicts the R-28 row's
   “`status` shows the error” as read literally. No product change made.
2. **Addendum in-gap `on` sample (evidence interpretation, no defect).** With call end
   times, no status call reports `on` before recovery; calls block or time out. The
   prior wording should not be read as a false active status.
3. No other defect observed. Fail-open recovery, fail-closed approved paths, bypass
   rebinding and restore all behaved as previously accepted.

## Limits

- Synthetic local fixtures only; no public exit/resolver, no real subscription, no user
  pool; results are not an internet/real-exit claim.
- Headless container: no tray/icon observation. The fail-open and R-28 status findings
  are about the status fields the tray consumes, not rendered icons.
- One kill path (SIGKILL of the TUN service) and one network-change shape
  (`default → dummy0`); suspend/resume, Wi-Fi→Ethernet at the driver level, and a
  second full-tunnel VPN were not exercised.
- Single architecture (amd64) runtime; arm64 runtime remains untested.
- The first `failopen/` pass (start-time classification only) is kept for comparison;
  `failopen-timed/` is authoritative.

## Evidence

`spike/tun/evidence/post-v1.2.0-verification-2026-11-27/`

- `h-addendum/` — stock addendum rerun (IPv6 DNS, package, single fail-open sample).
- `failopen-timed/` — 4 SIGKILLs with start/end-timed JSON and text status samples.
- `failopen/` — first pass, start-time classification only (superseded).
- `r28-offlink/` — topology, step log, status detail, results.
- `start-limit-confirm.txt`, `isolation.txt`, `final-host-check.txt`.
- `harness/` — the four evidence-only scripts used for the extra checks.

## Cleanup

All `rl-ha-post120v-20261127-*` containers and both networks were removed. The host has
no `rl-tun0`, no `198.18/15` or `fc00::/64` route, its only default is via
`wlp0s20f3`, and Tailscale remains stopped. The repository contains only this report
and the evidence directory; product code, docs and the plan are untouched.

## Addendum — post-fix rerun at `2d4da2a` (2026-11-27)

**Scope and revision.** Verification only. The binary was built from `2d4da2a`
(`fix(control): record only exit_unreachable as the egress warning`) in a fresh
disposable privileged systemd container with an isolated netns; no host
network/DNS/firewall/systemd/desktop change, no public exit/resolver, no subscription,
credential or user pool. The earlier report's §2 “Conformance gap (not fixed …)” text
describes the tested revision `899b036` and is left unchanged as history. **This
addendum supersedes only its status-visibility conclusion: the fixed warning appears
in the real off-link failure at `2d4da2a`.** The earlier safety observations
(fail-closed, no direct fallback, no loop) still stand.

**Topology and environment.** Client A (`172.31.78.10`) and router peer R
(`172.31.78.21` on A's segment, `172.31.79.2` on an internal `172.31.79.0/24`
bridge). R owns the off-link exit `172.31.79.20:11080` and the synthetic source
`203.0.113.7/8:7110`; A has no `172.31.79.0/24` route and reaches the exit through
the default gateway. Host isolation was re-confirmed before and after: one host
default via the Wi-Fi interface, no IPv6 default, Tailscale stopped, no `rl-tun0`,
and no host LAN/VPN route inside A. A's only links are `lo`/`eth0`; public egress
attempts (`1.1.1.1:443`, `93.184.216.34:80`, `8.8.8.8:53`) timed out with the peer's
forwarding off. Image `rl-tun-spike:base` (`sha256:cbf0ea96…`), Ubuntu 26.04.1,
kernel `7.0.0-44-generic`, systemd `259.9`, Docker `29.10.1`. Binary SHA-256
`1caac466…`.

**Break and warning.** The prior break shape was reproduced: add `dummy0`
(`10.255.255.1/32`) and replace the default via the gateway with `dummy0`.
`ip route get 172.31.79.20 oif eth0` then falls back to `172.31.79.20 dev eth0`
with no connected `172.31.79.0/24`; the exit is reachable only by ARP on the shared
segment, which the peer deliberately does not answer (`arp_ignore=1`). Before the
break, `status` had only `tun_dns_direct`/`tun_udp_blocked`; immediately after the
route change, still only those two. The first approved flow after the change failed
closed (`eof`, 3498 ms; a second run 3486 ms), no direct accept and no exit CONNECT.
The **next `status` sample** (16:33:15.778Z, after the failed dial) reported:

- `status --json` → `data.warnings` contains the exact fixed sentence
  `recent approved egress dial failed; traffic remains fail-closed with no direct fallback`
  (plus the two TUN warnings);
- human `status` → the same line as `warning: …` on **stderr** (stdout body unchanged,
  matching `TestPrintStatusPrintsEgressWarning`);
- `tun status` → `state=on, desired=true, helper=installed, last_error=null,
  detail=null`; it remains coverage-only and does not carry the egress warning;
- `pool list` → `healthy=1`, `consecutive_failures=0` at that moment. The warning
  itself does not mutate health.

**TTL and clear.** A later approved failure reset the timestamp. The warning was
still present in the `status` sample 30 s later, and absent at 62.1 s, with no
successful approved dial in between (`status.warnings` still carried the separate
pool-unavailable line). The warning also survived `tun off` inside the TTL and
`tun on` before recovery, then disappeared on the first successful approved dial
(429 ms) after the default was restored and `tun off/on` rebound the bypass. Those
are the documented 60 s TTL and clear-on-success behaviors.

**Health nuance (separate from the warning).** While the break persisted, automatic
diagnostics probed the still-unreachable exit about 17 s after the failed approved
dial and recorded one `exit_unreachable` failure, putting the record into a 60 s
cooldown; `status` then also showed “pool has no usable exit”. This is the existing
diagnostics/health action, not the warning (the warning is observational and does
not cool, rerank or change health). After the route restore, `tun off/on`, and the
cooldown expiry, a final approved flow recovered (429 ms, peer `203.0.113.7`) and
the pool record returned to healthy; the direct path recovered at 215 ms. The
earlier report's “pool stays healthy” observation was true at the instant after the
failed dial; this longer run shows the later diagnostics cooldown as well.

**Failure-kind attribution.** The warning appeared, so the recorded class was
`exit_unreachable`, the only class the fixed code records; it was not a
timeout/cancel and no destination-leaking log was added. The client-visible `eof`
arrives after ~3.5 s, consistent with the off-link ARP/connect path (the prior
evidence recorded the ARP entry `INCOMPLETE`); a refused transport would fail
immediately. The prior NOTRUN reflects sampling before the failed dial finished; this run
samples after it and reads `data.warnings` (the first harness pass here also read
the envelope by mistake and was corrected before the recorded run).

**Result.** Off-link R-28 post-fix harness: **36 PASS, 0 FAIL, 0 NOTRUN**. Safety is
unchanged: approved traffic fails closed with no direct fallback, no loop and no
exit CONNECT; the unapproved destination still takes its allowed stale direct path;
recovery follows route restore plus `tun off/on` (and, for the persistent break, the
diagnostics cooldown).

**Evidence.** `spike/tun/evidence/post-v1.2.0-verification-2026-11-27/post-fix-2d4da2a/`:
`results.txt`, `steps.txt`, `samples.jsonl`, `warning-observation.txt`,
`status-*.json`, `isolation.txt`, `environment.txt`, `topology.txt`, `cleanup.txt`,
`host-cleanup.txt`, and `harness/r28_offlink_fix.py`. All `rl-r28fix-*` containers
and networks were removed; the host has no `rl-tun0`, no `198.18/15` or
`fc00::/64` route, its only default is the Wi-Fi interface, and Tailscale remains
stopped.

**Limits.** Synthetic local fixtures only; no public exit/resolver, real
subscription or user pool. One break shape (`default → dummy0`), one architecture
(amd64) runtime, headless (no tray), and one TTL observation; the warning is
in-memory and is not expected to survive a service restart (not exercised here).
