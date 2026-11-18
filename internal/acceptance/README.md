# Acceptance suite (T7)

This package drives the **shipped binary** as a real process against
loopback-only fixtures and records what each acceptance item can prove, what
fails, and what cannot be proven in this environment. It is a client of the
product: it imports no product runtime package and reaches the service only
through the CLI/management surface and the two loopback traffic listeners.

## Command

```sh
export PATH=$PWD/.toolchain/go/bin:$PATH GOTOOLCHAIN=local \
       GOMODCACHE=$PWD/.toolchain/modcache GOPROXY=direct
go test -count=1 -v ./internal/acceptance/     # PASS/FAIL lines go to stdout
```

Runtime was 114–144 s across two R6 full runs (pre-R6: 110–130 s); the three
transport cells add about 5 s. Two waits dominate: `proposals recheck` waits out the
prober's hard-coded 60 s destination cooldown, and A07 waits one 30 s rotation
tick to observe startup health. Run `-v -run TestEndToEnd` for the main flow, or
`-v -run 'TestFailureAndLimits|TestResourceBaseline'` for the cheap checks.

### Gated real-systemd check (R1)

One test needs a real `systemd --user` manager and installs a temporary unit, so
it is skipped by default and stays out of the offline suite:

```sh
ROUTELATCH_SYSTEMD_TEST=1 go test -count=1 -v -run TestA13SystemdUserLifecycle ./internal/acceptance/
```

It takes the shipped `deploy/systemd/routelatch.service`, points `ExecStart` at
the built binary plus an isolated `--data-dir` and random loopback ports, writes
that unit into the caller's real user-manager unit directory and removes it
afterwards (`systemctl --user daemon-reload` is part of the run). It asserts
start/restart, loopback-only listeners, a foreign listener on the configured
HTTP port (exit 6, `NRestarts=0`, no fallback), SIGKILL followed by the
on-failure restart with byte-identical state and no silent direct, and a clean
exit 0 stop. Only run it where installing a user service is authorized; a
reboot/re-login is never performed.

### Gated real-desktop check (R2)

The desktop adapter also has a gated test that changes this machine's **real**
KDE or GNOME proxy setting, so it is skipped by default:

```sh
ROUTELATCH_DESKTOP_TEST=1 go test -count=1 -v -run TestA11DesktopProxyLifecycle ./internal/acceptance/
```

Run it from inside the desktop session (it detects KDE/GNOME from the session
environment), on a machine whose desktop proxy you may change and repair. It
captures a snapshot first, applies the proxy, reads the OS configuration back,
stops the service, checks that the desktop still points at RouteLatch (never
silently direct) and that the closed listener refuses connections, restores
offline, and compares the OS setting with the snapshot. A deferred cleanup
restores the snapshot even if an assertion fails, but `kill -9` of the test
process would skip it, so do not run it unattended.

### Gated real hysteria2 traffic check (R11)

The default suite proves the R11 import projection, the handoff config shape
and the fail-closed exclusion with a local fake sidecar (a helper mode of this
test binary started by a small wrapper script). No binary, network or system
state is involved there. The real traffic cell is skipped by default:

```sh
RL_ACCEPT_SINGBOX_EXEC=/path/to/verified/sing-box \
RL_ACCEPT_HYSTERIA2_TEST=1 go test -count=1 -v -run TestR11Hysteria2RealTraffic ./internal/acceptance/
```

Use **either** `RL_ACCEPT_SINGBOX_EXEC` (a binary you already verified) or let
the test download and verify it itself. With no `ROUTELATCH_SINGBOX_EXEC`, the test
fetches the **official** sing-box v1.14.4 archive from the upstream release,
verifies its SHA-256 against the digest pinned in `scripts/package.sh` and
`packaging/README.md`, and extracts the binary into a disposable directory
under `/tmp/opencode` (removed afterwards). A download or checksum failure is
reported as a `NOTE ... real-traffic NOT RUN` with the exact reason — it is
never counted as a pass. The verified binary is used only as a loopback
hysteria2 server and as the RouteLatch sidecar; nothing is installed, no
service, proxy, DNS or desktop setting is touched. A test-generated CA is
trusted by the test process only through `SSL_CERT_FILE` (no root CA is
installed); TLS verification stays on in both RouteLatch and sing-box.

That cell drives a real HTTP CONNECT through RouteLatch → the real sing-box
sidecar → a real hysteria2 inbound → a loopback echo peer, asserts a
byte-identical echo with exactly one destination contact, then checks the
dead-exit and absent-sidecar 502 paths (0 destination contacts), an unapproved
destination going direct, and the D21 canary in state, sidecar config, service
streams and CLI output.

## Fixtures

* The test builds `./cmd/routelatch` once (`TestMain`) and starts it with
  `run --data-dir <tmp>`; each test has its own temp `HOME`, `XDG_CONFIG_HOME`,
  `XDG_STATE_HOME`, `XDG_RUNTIME_DIR` and data dir.
* A local fixture file carries a VLESS node, a Trojan node and a SOCKS5 node.
  The VLESS/Trojan exits are **child processes** of this test binary running the
  pinned core (the T1 technique), each with static DNS entries so the *exit*
  resolves the fixture names to `127.0.0.1`. The host resolver cannot resolve
  those names, so a successful tunnel proves the hostname reached the exit
  unresolved.
* The optional R6 matrix starts a separate pinned-core exit child per transport
  (WS, gRPC, XHTTP), each on TLS with a per-run self-signed leaf, an explicit
  ALPN (`http/1.1` for WS, `h2` for gRPC/XHTTP) and a local destination peer.
  Client and exit never share a process: gRPC/XHTTP keep process-global dialer
  state across `core.Stop`. The imported links pin the leaf; verification is
  never disabled.
* The SOCKS5 exits are small servers owned by the test. They record each CONNECT
  authority, dial `127.0.0.1:<requested port>` (the destination peer) and record
  when the product closes the exit-side socket. A "stall" host holds the
  handshake so a **pending pool dial** is observable; a second exit stays
  unknown so health-based selection is testable.
* Destinations are loopback TCP echo/HTTP/streaming peers that count contacts
  and record teardown. Public-looking destinations use documentation IPv6
  addresses (`2001:db8::/32`); this host has no IPv6 route, so a direct dial
  fails with `ENETUNREACH` and no packet leaves the machine.

No public exit, credential, service installation or system setting is used by
the default suite. The two real-system checks above are gated and skipped unless
their environment variable is set; only the gated desktop check can change a
desktop proxy setting.

## Commands exercised

`run`, `status`, `mode targeted|all-proxy|all-direct|stopped`,
`rules list|add|remove`, `pool list|remove|select|test|refresh|add --file`,
`import file|link|subscription|v2rayn`, `diagnose`,
`proposals list|accept|reject|recheck`, `config get|path|set`, `doctor`, `env`,
`exec`, `backup`, `restore`, `desktop proxy status`, `menu` — each with
`--json --data-dir <tmp>`; assertions include the exit codes
0/1/2/3/4/6/8/10 and, where the contract names one, the envelope `kind`.

## Proven (PASS lines carry the counts)

| Item | Assertions |
| --- | --- |
| A02 | Exact-domain, domain-suffix, IP and CIDR rules accepted; each routes through the exit (exit dial +1 per case, untouched authority recorded by the exit); the suffix rule does not match `xsuffix.rl.test`; port-bearing and loopback rule operands are refused (2/6/7) and store nothing; an unapproved public destination stays direct (0 pool dials/0 contacts); a loopback direct destination works with no exit. |
| A03 | A pool with only a quarantined record (usable 0) and a truly empty pool both give `502`, 0 destination contacts and 0 exit dials before and after the SOCKS5 attempt; the direct destination still works; `0x04` for an approved destination and `0x05` for a directly refused one. |
| A04 | Local-exception tunnels survive mode changes (kept ≥ 1, still echo); all-proxy routes an unapproved public destination through the pool; all-direct serves without an exit; a live **pool** tunnel is closed by targeted→all-direct and by `rules remove` (both ends observe teardown, `closed ≥ 1`); `stopped` closes the live tunnel and both ports, reports 0 open/0 dialing immediately, keeps management answering, and leaving stopped is exit 6 with the restart hint; the pending-dial pass path enforces the reported `duration_ms` ≤ 50 ms (CLI round trip loosely ≤ 1 s). |
| A05 | One client request reaches the origin exactly once; a refused direct request (path/query/header nonce) leaves no application request at the origin and no bytes in proposals, state, CLI or the service log; an eligible failure creates a proposal with no rule; `accept` adds exactly one rule for exactly that destination and the next request uses the pool; `reject` keeps the rule set unchanged and `recheck` does not revive it; disabling diagnostics leaves queue 0 and creates no new proposal. **D21 canary:** one unique unapproved destination goes through a failed direct request, the automatic probe, the live `proposals list`, reject/recheck, a mode switch and a backup, then a restart; it appears in no file under the temp HOME/XDG tree (state, config, backups, log and rotations, locks) and in no captured service stdout/stderr, and `proposals list` is empty after the restart (proposals are process memory only). A second canary that the user approves appears only in the approved rule set. |
| A06 | File import = 3 valid/0 invalid/0 unsupported; per-protocol `import link` (VLESS/Trojan/SOCKS5) each valid 1 and merged to 3 records; Trojan credentials drive a live tunnel; a v2rayN JSON export imports 3 profiles and keeps its source attribution; a skip-verify link is `unsupported`/`insecure_tls_removed`, unselectable, and `doctor` reports it with a certificate/pin remediation; a certificate-pinned TLS exit imports and carries a real tunnel, a wrong pin fails on the first byte with 0 destination contacts, the same link with `allowInsecure` is accepted with an `allow_insecure_ignored` notice and stays usable, and a restart on the same state still starts and serves (D18); a failed subscription refresh (exit 1, `source_fetch`) leaves every row's identity/status/settings unchanged. |
| A07 | Cold start has no health; the first rotation cools the dead exit; an egress probe with byte evidence promotes the live exit to healthy; `pool rerank` reports the healthy selection, the skip list and an unchanged policy version; with no pin the approved destination uses the healthy exit, skips the cooling one and does not pick the unknown one; `pool test`, `select`, `refresh` and `rerank` keep the process start time and a live compatible **pool** tunnel; an all-cooling pool gives `502` with 0 contacts and a no-usable-exit warning; a failed refresh keeps last-good. |
| A08 | Fixture names do not resolve on the host; the VLESS exit logs its own DNS answer for `vless.rl.test`; the SOCKS exit receives the unresolved hostname. |
| A09 | The process's own socket table has exactly two listening TCP sockets, both loopback, and management only as a Unix socket owned by the running uid with mode 0600; proxy-to-self is 403 `policy_denied`; directories 0700 and state/config/lock/`state.json.bak`/`log/routelatch.log` 0600; every captured CLI/log surface has no UUID/password/share link/subscription URL and no request nonce; a node title carrying ESC/CSI/OSC/BEL/CR appears sanitized in both pool tables, its printable text intact, and the service streams and log file carry no raw control byte; state carries secrets only in `records[].credentials`/`sources[].origin`. |
| A10 | `env`, `exec` (proxy env + child exit code; with `--json` the child's streams stay on stderr and stdout is one parseable Envelope per §7.1), `doctor`, `backup`/`restore` (private ref + sha256, traffic and rules after restore), `desktop proxy status`, `config get|path|set` (values and private paths; wildcard and valid LAN listeners are both `unsupported`/7), and the non-interactive menu (exit 0). |
| A11 | An inherited `NO_PROXY` naming an approved destination is replaced by the product's fixed list in the child; with listeners stopped, and with no service at all, `env`/`exec` exit 3 and the child never runs (no silent direct). |
| A11 (gated desktop) | `ROUTELATCH_DESKTOP_TEST=1`: the real KDE/GNOME setting is applied and read back from the OS, the state records the previous value, stopping the service leaves the desktop pointing at RouteLatch (never silent direct) with a closed listener, offline restore returns the exact snapshot, and the marker stops claiming ownership. |
| A12 | HEAD (headers only), POST with body, chunked request framing, an incremental streaming response (first bytes before the origin finishes), an unbounded stream (64 KiB in ~0.35 s, no cap), SOCKS5 UDP/BIND `0x07` with no UDP socket in the process, an absolute `https` request refused with exactly 501 and zero origin accepts/bytes (no MITM), and an unsupported scheme 405/501. |
| A13 | SIGKILL with an idle tunnel and an in-flight streaming request (the request is cut, not completed): state byte-identical after restart, 3 exits/2 rules usable, approved and direct tunnels work; corrupt state/config exit 10 without overwrite; newer schema rejected; second instance exit 4 with the first still serving; port collision exit 6 with no fallback; SIGINT exit 0 with state byte-identical; stopped mode persists across SIGKILL+restart and a new process may leave it once. |
| A13 (gated systemd) | `ROUTELATCH_SYSTEMD_TEST=1`: the shipped unit runs under a real `systemd --user` manager; start/restart replace the main pid and both listeners answer; a foreign listener on the configured HTTP port yields exit 6, `NRestarts=0`, the foreign listener still serves and no SOCKS/fallback listener appears; SIGKILL triggers the unit's on-failure restart with byte-identical state and the approved destination still answers 502 `pool unavailable` (no silent direct); `systemctl stop` exits 0 and closes both listeners. |
| R5 (legacy status) | An undialable protocol persisted as `unknown`, `healthy`, `cooling` (expired or live cooldown), or `unsupported`/`engine_record_rejected` reports `unsupported` with the importer's reason in `pool list` rows and counts, `status` warnings and `doctor`; a stored `invalid` stays `invalid`; a supported-protocol record keeps `engine_record_rejected`; `pool test` rejects each before any TCP connection (0 endpoint connections); the derivation never persists `unsupported` (the only write after a probe is the documented cooldown expiry). |
| R6 (optional) | WS/TLS (`http/1.1`), gRPC/TLS (`h2`) and XHTTP/TLS (`h2`) each: a pinned link imports as valid, the approved rule and `pool select` use it, and one CONNECT request reaches a loopback HTTP destination and returns its 207, header and body unmodified (exactly 1 destination request). A flipped pin delivers no destination request, leaves the service answering, and the good pin still tunnels afterward. |
| R11 | Default-on fake-sidecar cell: `hysteria2://` and a sing-box hysteria2 outbound JSON import through the real CLI, a skip-verify link is quarantined `insecure_tls_removed`; the generated handoff pins one hysteria2 outbound with `insecure:false`, no `resolve` rules and a block final; with no executable the stored exit is `unsupported`/`sidecar_unavailable` and gives 502 with 0 destination contacts while Xray still imports and dials. The gated real-traffic cell (opt-in above) adds a digest-verified v1.14.4 binary, a real TLS handshake to a loopback hysteria2 server, a byte-identical echo with exactly 1 destination contact, the dead-exit 502, direct-vs-approved routing and the D21 canary in state, sidecar config, streams and CLI output. |
| Resources | Product descriptors return to idle baseline after ten tunnels (9→9/9→10, tolerance +2); the test process's goroutines return to exact baseline (3→3) after fixtures and service stop. |

## Findings in the product

* **F1 (found here, fixed in `3c2817e`, regression check)** — every pool tunnel
  died right after the reply because the returned session was owned by a
  dial-scoped context (10 s deadline + deferred cancel). The suite now asserts a
  working byte stream on HTTP-via-SOCKS5, HTTP-via-VLESS and the SOCKS5 frontend.
* **F2 (found here, fixed in `3c2817e`)** — `probes.ErrProbeCooldown` escaped the
  diagnostic error mapping as `kind=internal`, exit 1. The suite now requires the
  documented conflict (exit 6) for a recheck inside the probe cooldown.
* **F3 (found here, fixed in `3c2817e`)** — the policy fence was held across the
  pool dial, so a mode change returned exit 8 after ~5 s and applied at ~9.8 s.
  The suite requires `duration_ms ≤ 50 ms` and the pending dial to be swept
  before success.
* **F4 (found here, fixed in `659064f`)** — `exec --json` glued its envelope
  to unterminated child stdout, so stdout was not valid JSON. The fix routes the
  child's stdout/stderr to stderr in JSON mode and documents it in §7.1; the
  suite now asserts that separation and a parseable envelope.
* **F5 (found here, fixed in `659064f`)** — the documented log file was never
  created: §4.1 lists `$XDG_STATE_HOME/routelatch/log/routelatch.log` with mode
  0600 and T6 requires rotation/retention, but no code opened `Paths.LogDir`. The
  suite now asserts the file, its mode and that it carries no control byte.
* **F6 (found here, fixed in `659064f`)** — a valid but non-loopback listener was
  classified as `invalid_destination`/exit 2 via `usage(...)`, while §4.2 requires
  `unsupported`/exit 7. It is now `unsupported`/7 for both the wildcard and a LAN
  address at `config set` (asserted), and the runtime listener check
  (`platform.Targets.Validate`) returns the same kind (`run` with
  `http_listen=0.0.0.0:1000` exits 7 without binding, observed manually). §7.1's
  `config get|set|path` row now lists 0/2/3/6/7.
* **F7 (found here, fixed in `659064f`)** — absolute-form `https` was refused
  with `policy_denied`/403, while §3.1 reserves that kind for self/local
  prohibition and an operation outside v1 maps to `unsupported`/405–501. It is
  now `unsupported`/501 with no origin accept or byte (asserted).
* **F8 (found here, fixed in `087879d`)** — a `pinSHA256` link
  that also carries `allowInsecure=1` was rejected as `unsupported`/exit 7
  ("skip-verify TLS is not dialable in v1") although §5.5 requires acceptance
  with the pin keeping verification on; the rejected import had already written a
  record with `insecure_tls: true`, so the next `run` on that data dir failed with
  exit 7 (the service was bricked until the record was removed). The current
  fix stores the pin without the insecure flag, reports
  `allow_insecure_ignored`, and the D18 check asserts import, usability, the
  wrong-pin failure and restart.

Shape assertions that follow implementation names rather than a §7.1 field list
(doctor check names and `ok|warn|error`, the `<data-dir>/run/control.sock` layout,
`ref.checksum`) are stability assertions: a contract-compliant rename would fail
them until the suite is updated.

## Cannot be proven in this environment

* **A01 / A14:** a clean machine with no manual runtime (A01) and the release
  build/source package (A14) remain; the license decision and real binary audit
  are recorded in [`docs/licensing.md`](../docs/licensing.md).
* **A02 matrix:** the rest of §1.5/§2.5 is not enumerated (one negative suffix
  boundary plus unapproved public/IP cases); public IPv4 egress is deliberately
  never attempted (it would leave the machine).
* **A03:** a missing engine tag and the §2.7 stale-version fence are not
  exercised (empty, zero-usable and all-cooling pools are).
* **A04 direct-public rows:** closing a live *direct* connection to a public
  destination when it becomes approved needs local DNS control; the pool rows,
  rule removal and stopped are covered.
* **A05 wire replay:** an eligible direct failure is pre-reply by definition, so
  no origin can receive that request; the suite proves one forward on success,
  no request material in evidence/state/output and no pool dial from the failure
  or diagnosis, not a negative at the origin. Cancelling an already-running slow
  probe on disable is not reproducible loopback-only.
* **A06:** v2rayN SQLite/`guiNConfig` rejection and `pool export --with-secrets`
  are not exercised; the pin checks use a self-signed fixture certificate, not a
  public CA chain or a pinned CA.
* **A07:** the explicit `pool rerank` path is exercised, but the 10-minute
  rotation, EWMA/hysteresis switching, latency thresholds and the plan's
  staging/rollback are not.
* **A09:** second-uid `SO_PEERCRED` rejection needs privileges the suite does
  not have (running as the same uid); the interactive menu's TUI rendering is not
  driven (CLI human output, the service streams and the log file are).
* **A10:** menu item prompts/mappings and the 5/9/11/12 exit codes are not
  exercised (exit 7 is asserted by F6); the real desktop `set|restore` is only in
  the gated A11 desktop check.
* **A11 desktop:** real GNOME/KDE set/restore/ownership is covered by the gated
  `ROUTELATCH_DESKTOP_TEST=1` check, not by the default offline suite; the
  ownership-conflict and `kill -9` desktop sequences are recorded as manual R2
  evidence rather than automated here.
* **A12:** redirects, `101` upgrades, slowloris/size limits and real IPv6 egress
  are not exercised; UDP is asserted only as an explicit refusal.
* **A13 / platform:** systemd lifecycle is covered by the gated test above and
  the R1 install run. A real reboot/re-login is not executed, and linger was
  already enabled on the R1 machine, so only `enable`'s symlink and the
  `WantedBy=default.target` configuration were inspected for login start-up.
  Windows, Android and TUN are out of scope.
* **R11 real traffic:** the real hysteria2 handshake needs the opt-in cell above;
  the default suite proves only the import shape, the generated handoff config
  and the fail-closed exclusion with a fake sidecar. The gated run cannot use a
  public hysteria2 exit or a system CA, the pinned leaf is a locally generated
  certificate trusted only via `SSL_CERT_FILE`, and only one loopback server and
  one destination peer are exercised (no obfs, bandwidth negotiation or roaming).
* **Transport limits:** WS/gRPC/XHTTP are runtime-verified only over TLS against a
  local pinned exit, with the ALPN above and one VLESS stream per test
  request. REALITY/ECH, mKCP, HTTPUpgrade, XHTTP over h3/QUIC, and mux/XUDP are
  not exercised. One core per process is kept throughout, so these cells do not
  test multiple embedded cores, transport reuse counts, or long-lived gRPC/XHTTP
  session behavior.
* The product's own goroutine count is not externally observable; descriptors are
  the proxy. Traffic ports are chosen with a bind-close-rebind probe, so a
  concurrent process taking a port between probe and bind surfaces as a startup
  failure rather than a flaky assertion.
