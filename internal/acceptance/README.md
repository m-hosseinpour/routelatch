# Acceptance suite (T7)

This package drives the **shipped binary** as a real process against
loopback-only fixtures and checks the acceptance items it can prove in this
environment. It is a client of the product: it imports no product runtime
package and reaches the service only through the CLI/management surface and the
two loopback traffic listeners.

## Command

```sh
export PATH=$PWD/.toolchain/go/bin:$PATH GOTOOLCHAIN=local \
       GOMODCACHE=$PWD/.toolchain/modcache GOPROXY=direct
go test -count=1 -v ./internal/acceptance/     # PASS/FAIL lines go to stdout
```

Runtime is about 75 s: one subtest deliberately waits out the probe engine's
hard-coded 60 s destination cooldown to exercise `proposals recheck`. Run
`-v -run TestEndToEnd` for the main flow, or `-v -run 'TestFailureAndLimits|TestResourceBaseline'`
for the cheap failure/leak checks.

## Fixtures

* The test builds `./cmd/routelatch` once (`TestMain`) and starts it with
  `run --data-dir <tmp>`; each test has its own temp `HOME`, `XDG_CONFIG_HOME`,
  `XDG_STATE_HOME`, `XDG_RUNTIME_DIR` and data dir.
* A local fixture file is imported with three links: a VLESS node, a Trojan node
  and a SOCKS5 node. The VLESS/Trojan exits are **child processes** of this test
  binary running the pinned core (the T1 technique), each with a static DNS host
  entry so the *exit* resolves `vless.rl.test` / `trojan.rl.test` to `127.0.0.1`.
  The host resolver cannot resolve those names, so a successful tunnel proves the
  hostname reached the exit unresolved.
* The SOCKS5 exit is a small server owned by the test. It records each CONNECT
  authority, dials `127.0.0.1:<requested port>` (the loopback destination peer),
  and records when the product closes the exit-side socket. A "stall" host makes
  it hold the handshake so a **pending pool dial** can be observed.
* Destinations are loopback TCP echo/HTTP peers that count contacts and record
  teardown. Public-looking destinations use documentation addresses
  (`2001:db8::/32`); this host has no IPv6 route, so a direct dial fails with
  `ENETUNREACH` and no packet leaves the machine.

No public exit, credential, service installation, desktop proxy change or system
setting is used.

## Commands exercised

`run`, `status`, `mode targeted|all-proxy|all-direct|stopped`,
`rules list|add`, `pool list|remove|select|test|refresh|add --file`,
`import file|subscription`, `diagnose`, `proposals list|accept|reject|recheck`,
`config set` — each with `--json --data-dir <tmp>`; assertions include the exit
codes 0/1/2/3/4/6/8/10 and, where the contract names one, the envelope `kind`.

## What the suite proves (PASS lines carry the counts)

| Item | Assertions |
| --- | --- |
| A02 | Exact-domain, domain-suffix, IP and CIDR rules accepted; each routes through the exit (exit dial count +1 per case, untouched authority recorded by the exit); a port-bearing rule operand and a loopback rule operand are refused with a documented code (2/6/7) and store nothing; an unapproved public destination stays direct (0 pool dials, 0 destination contacts); the plain loopback destination works and uses no exit. |
| A03 | A pool holding only a quarantined record (total 1, usable 0) and a truly empty pool both give `502` for an approved destination, 0 destination contacts and 0 exit dials (no direct fallback) — checked before and after the SOCKS5 attempt; the direct destination still works; `0x04` on SOCKS5 and `0x05` for a directly refused destination. |
| A05 | A refused direct destination produces a proposal for exactly that destination and no rule; request path/query/header/cookie nonce absent from proposal output and state; manual `diagnose` is eligible; `accept` adds exactly one rule and the next request to that destination uses the pool (exit dial +1, destination contact +1); `reject` keeps it rejected; `recheck` re-probes after the cooldown and a rejected proposal stays rejected; disabling diagnostics leaves queue 0 and creates no new proposal. |
| A06 | File import = 3 valid / 0 invalid / 0 unsupported; the same links over a local subscription merge to 3 records (dedupe); a skip-verify link is quarantined as `unsupported` with reason `insecure_tls_removed`, cannot be selected as an exit (exit 6) and shows the TLS-verification warning; a failed subscription refresh reports `source_fetch` exit 1 and leaves every row's identity, status and settings unchanged. |
| A08 | Fixture names do not resolve on the host; the VLESS egress probe succeeds and the exit log shows its own DNS answer (`vless.rl.test -> 127.0.0.1`); the SOCKS exit receives the unresolved hostname and reaches the destination. |
| A09 | The product process's own socket table shows exactly two listening TCP sockets, both loopback, and management only as a Unix socket; CONNECT to our own listener is refused with 403 `policy_denied` (the matrix routes it as `self_loop`, but that reason is not on the wire by design, so it is not asserted); the socket is 0600; every created data/runtime directory is 0700; state/config/lock/state.json.bak files 0600; every CLI/log surface captured up to that pre-shutdown scan contains no UUID, password, share link or subscription URL, and none contains the user request nonce (path/query/header/cookie), checked again after the proposal decisions; state files carry secrets only in `records[].credentials` / `sources[].origin`. |
| A12 | A plain HTTP forward preserves status 207, a destination header and the body byte for byte. |
| A04 | `targeted→all-proxy` and `all-proxy→targeted` keep a live local-exception tunnel (kept ≥ 1, still echoes); all-proxy routes an unapproved public destination through the pool; all-direct keeps serving and uses no exit; `stopped` closes the live tunnel (both ends observe teardown), reports 0 open/0 dialing connections immediately after success, closes both proxy ports, keeps management answering, and leaving stopped is exit 6 with the documented restart hint. The pending-dial pass path also enforces the command's reported `duration_ms` ≤50 ms (the CLI round trip is only loosely bounded at 1 s); F3 fails it. |
| A13 | Corrupt state and corrupt config each exit 10 with the file byte-identical (state still 0600); a newer schema is rejected the same way; a second instance exits 4 and leaves the first serving; an occupied port exits 6, names the listener, and leaves no fallback listener; SIGINT exits 0 and leaves the committed state byte-identical. |
| A10 | JSON envelopes and exit codes are asserted on every command used (0/1/2/3/4/6/8/10); `status` after shutdown exits 3. |
| Resources | After ten tunnels the product's descriptor count returns to its idle baseline (observed 9 → 9 or 9 → 10 depending on the run, tolerance +2); after the service and every fixture stop, the test process's goroutine count returns exactly to baseline (observed 3 → 3, tolerance +2). |

## Findings in the product (reported, not fixed)

* **F1 — every pool tunnel dies immediately after the reply (blocks usable pool
  traffic).** `internal/frontend/frontend.go` creates the pool dial with
  `context.WithTimeout(ctx, PoolDialDeadline)` + `defer cancel()` and passes it
  to `DialPool`; `internal/control/service.go` forwards that same context to
  `engine.Dial`; `internal/engine/dial.go` builds the core session from it and
  `internal/engine/socks.go` arms `adapterConn` on it. The returned session is
  therefore owned by a dial-scoped context: the deferred cancel tears it down at
  return, and even without the defer the 10 s dial deadline would kill the
  tunnel later (forbidden by §3.4). Observed on **all three client-visible
  paths**: HTTP CONNECT via the SOCKS5 exit (`200`, exit CONNECT accepted,
  destination contacted once, then the exit socket closes and the client reads
  `EOF` with 0 payload bytes), HTTP CONNECT via the VLESS core exit (`200`, no
  payload byte), and the SOCKS5 frontend (`0x00`, no payload byte). `pool test
  --target` (no frontend dial context) succeeds, which isolates the cause.
* **F2 — probe cooldown surfaces as an internal error.** After a `diagnose`,
  `proposals recheck` between the diagnostics cooldown and the prober's fixed
  60 s destination cooldown returns `kind="internal"`, exit 1, message
  `destination probe cooldown has not elapsed`. `mapDiagnosticsError` maps only
  `diagnostics.ErrCooldown`; `probes.ErrProbeCooldown` escapes. Exit 1 is not in
  the documented `proposals` exit set (0/3/5/6).
* **F3 — a pending pool dial makes a mode change exceed the management
  deadline.** With one stalled pool dial, `mode all-direct` returned exit 8
  (`timeout`) after 5.03 s and applied the change at +9.84 s. `Service.fenceMu`
  is read-held for the whole dial, so the policy commit waits for
  `PoolDialDeadline` (10 s); §3.4 requires a pending dial to be canceled in
  ~50 ms and the sweep to finish before success is reported. The exit-side
  socket close observed at +9.83 s is the dial's own deadline firing, not a
  sweep cancel.

## Not verified here

* **A01/A11/A14:** clean-machine install without a manual runtime, desktop proxy
  set/restore, and the licensing/dependency audit are outside this environment.
* **A02:** the rest of the §1.5/§2.5 matrix is not enumerated (only one negative
  suffix boundary plus the unapproved public/IP cases); IPv4 public egress is
  deliberately never attempted.
* **A03:** a dead exit and a missing engine tag are not exercised (the empty and
  zero-usable pools are).
* **A04:** §2.6 rows that need a live **pool** connection (mode switch, rule
  add/remove) are blocked by F1; the direct-public rows need local DNS control
  and are blocked by the no-public-egress fixture; the ≤50 ms pending-cancel
  bound is measured only as a failure (F3), and `stopped` repeats neither a
  pending dial nor a pool connection.
* **A05:** "disable cancels an already-running probe" is not reproducible
  loopback-only (only queue 0 and no-new-proposal are checked); wire-level "no
  request replay" is not observable here — the suite shows the failure and the
  absence of request material, and the product's probe is a bare TCP dial.
* **A06:** `import link` and the v2rayN path are not exercised; `doctor`
  visibility of the quarantined record is not checked (pool list and status are).
* **A07:** ranking, cooldown growth, hysteresis and last-good rotation are not
  exercised.
* **A09:** the same-uid `SO_PEERCRED` check, terminal-control-sequence
  sanitization, log-file creation/mode, and request *body* bytes in logs are not
  observed; only the socket mode and the captured surfaces are.
* **A10:** the menu, and the §7.1 commands `env`, `exec`, `doctor`, `backup`,
  `restore`, `desktop`, `config get`, `rules remove` and the 5/7/9/11/12 exit
  codes are not exercised.
* **A12:** only one GET (status/header/body); HEAD/POST, streaming bodies,
  redirects and UDP are not.
* **A13:** systemd/launcher, crash recovery, a foreign listener's *behaviour*
  after the collision (only "no fallback listener" is checked) and restart
  persistence are not.
* The product's own goroutine count is not externally observable, so only its
  descriptors are compared to baseline.

The suite intentionally fails while F1–F3 are open: the FAIL lines (F1 on three
client-visible paths, F2, F3) are product findings, not harness errors. Traffic
ports are chosen with a bind-close-rebind probe, so a concurrent process grabbing
the port between the probe and the product's bind would show up as a startup
failure rather than a flaky assertion.
