# R16-J UDP fixture evidence (2026-11-27)

Controlled, disposable fixtures for `spike/tun/R16-J-UDP-RESEARCH.md`. They use
only loopback or a dedicated `--internal` Docker bridge, a synthetic UDP echo,
TEST-NET/`93.184.216.34` addresses bound to `lo`, and a test-only certificate.
No public exit, resolver, subscription, credential or host network/DNS/firewall
change was used.

## Versions and build

- Xray: pinned module `github.com/xtls/xray-core`
  `v1.260327.1-0.20261109052709-676eaf66f8f0`, commit
  `676eaf66f8f05d4d558ee7a74b4736623d3feeb9`, runtime `26.11.9`.
- sing-box: `v1.14.4`, revision `c005f35300ce76427dc285481ec5695f5b179cd2`.
- Build Xray from the repository (no file is changed):

  ```sh
  GOFLAGS=-mod=readonly .toolchain/go/bin/go build \
    -o "$WORK/bin/xray" github.com/xtls/xray-core/main
  cp /usr/bin/sing-box "$WORK/bin/sing-box"
  ```

- Container image: the R16-A spike image `rl-tun-spike:base`
  (`sha256:cbf0ea967ebe…`) on `ubuntu:26.04`. It contains Python 3, `ip` and
  `tc`. Use a disposable privileged container for the TUN fixture.

## Fixtures

- `run_all.py` — start local VLESS/Trojan/SOCKS5/hysteria2 servers and clients,
  send UDP through each path, then sizes and a `netem` comparison.
  `docker run --rm --privileged --network none -v "$WORK:/w" …`
- `run_bench.py` — clean and 20 ms/1 % `tc netem` benchmarks.
- `run_tun_udp.sh` — two-container internal bridge: app UDP through a real Xray
  TUN inbound to a loopback SOCKS UDP relay and a peer echo; then the relay is
  killed to test fail-closed and cleanup.
- `run_tun_latency.sh` — same TUN path with sniffing on (shipped) vs off.
- `test-vless-cone-off.sh` — plain VLESS UDP with `XRAY_CONE_DISABLED=true`.
- `gen-cert.sh` — creates `conf/cert.pem` and `conf/key.pem`; neither is
  committed. The hysteria2 client fixture uses `insecure: true` **only** for this
  local test; the product sidecar never disables verification.

TUN fixtures require `WORK=<this fixture directory>` and a privileged disposable
container. They create and remove a Docker network named `r16j-internal` and
containers `r16j-tun`/`r16j-peer`.

## Result files

`results/00-versions.txt`, `01-exit-matrix.txt`, `02-bench.txt`,
`03-tun-udp.txt`, `04-tun-latency.txt`, `05-vless-plain-udp-cone-off.txt`,
`06-quic-sniff-test.txt`, `07-source-locators.txt`.

The `WARNING: Error loading config file …` lines in raw Docker output are a host
client-config permission warning, not a fixture failure.
