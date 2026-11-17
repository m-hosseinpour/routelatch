# RouteLatch T1 frontend spike

Evidence for task T1 unit B: whether ready-made HTTP/CONNECT and SOCKS5
frontends can honour the listener guarantees in `docs/requirements.md` and
`docs/architecture.md`.

- Results and recommendation: [FINDINGS.md](FINDINGS.md)
- SOCKS5 detail: [socksfront/NOTES.md](socksfront/NOTES.md)
- Reproduce everything: `./run.sh` (loopback only; pinned local Go toolchain)
- Packages: `httpfront/` (goproxy v1.9.4 probes + hardened config),
  `socksfront/` (go-socks5 v0.1.4 probes, plus `minimal.go`, an
  experiment-only CONNECT server used to size the own-code fallback)

Nothing here is production code.
