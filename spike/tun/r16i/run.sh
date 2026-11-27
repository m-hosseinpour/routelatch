#!/usr/bin/env bash
# R16-I DNS-through-pool research harness (disposable containers only).
#
# It proves only transport/policy mechanics with local fixtures:
#   * pinned Xray TUN captures DNS/53 and routes it to a `dns` outbound
#   * the DNS outbound uses remote DoH / DNS-over-TCP through a local SOCKS exit
#   * the DoH/TCP upstream dial bypasses the TUN and never uses the system DNS
#   * a dead pool fails the query and does not fall back to direct DNS
# No host DNS/firewall/route/interface/service is changed. No public exit or
# public resolver is used. All containers/network are removed on exit.
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
R="$REPO/spike/tun/r16i"
DATE="$(date -u +%Y-%m-%d)"
RUN_ID="${RL_R16I_RUN_ID:-$(date -u +%Y%m%d%H%M%S)-$$}"
A="rl-r16i-${RUN_ID}-a"
B="rl-r16i-${RUN_ID}-b"
NET="rl-r16i-${RUN_ID}-net"
WORK="${RL_R16I_WORK:-/tmp/opencode/rl-r16i-${RUN_ID}}"
EVID="${RL_R16I_EVID:-$REPO/spike/tun/evidence/r16i-${DATE}}"
IMG="rl-tun-spike:base"
KEEP="${RL_R16I_KEEP:-0}"
GO="${GO:-$REPO/.toolchain/go/bin/go}"

export DOCKER_CONFIG="${DOCKER_CONFIG:-$WORK/dockerconfig}"
mkdir -p "$DOCKER_CONFIG" "$WORK" "$EVID"
if [ -d "$EVID" ] && [ -n "$(ls -A "$EVID" 2>/dev/null)" ] && [ "${RL_R16I_OVERWRITE:-0}" != "1" ]; then
  echo "evidence directory not empty: $EVID (set a new path or RL_R16I_OVERWRITE=1)" >&2
  exit 1
fi
rm -rf "$EVID"; mkdir -p "$EVID"

log() { printf '\n===== %s =====\n' "$*" | tee -a "$EVID/run.log" >&2; }
say() { printf '%s\n' "$*" | tee -a "$EVID/run.log" >&2; }
cap() { local f=$1; shift; { printf '\n$ %s\n' "$*"; "$@" 2>&1; } >>"$EVID/$f"; }
cap_sh_a() { local f=$1 c=$2; { printf '\n$ docker exec %s sh -c %q\n' "$A" "$c"; docker exec "$A" sh -c "$c" 2>&1 || true; } >>"$EVID/$f"; }
cap_sh_b() { local f=$1 c=$2; { printf '\n$ docker exec %s sh -c %q\n' "$B" "$c"; docker exec "$B" sh -c "$c" 2>&1 || true; } >>"$EVID/$f"; }

cleanup() {
  rc=$?
  set +e
  log "cleanup (exit $rc)"
  if [ "$KEEP" = "1" ]; then
    say "RL_R16I_KEEP=1: $A $B $NET kept"
  else
    docker rm -f "$A" "$B" >/dev/null 2>&1
    docker network rm "$NET" >/dev/null 2>&1
  fi
  {
    echo "host rl-tun0: $(ip link show rl-tun0 2>&1 || true)"
    echo "host rl-tun0 route count: $(ip route 2>/dev/null | grep -c 'rl-tun0' || true)"
    echo "host containers: $(docker ps -a --format '{{.Names}}' 2>/dev/null | grep -c '^rl-r16i-' || true)"
    echo "host networks: $(docker network ls --format '{{.Name}}' 2>/dev/null | grep -c '^rl-r16i-' || true)"
    echo "repo changed paths: $(git -C "$REPO" status --porcelain=v1 | wc -l)"
  } >"$EVID/99-final-cleanup.txt" 2>&1
  # sanitize private host paths from evidence
  if [ -d "$EVID" ]; then
    find "$EVID" -maxdepth 1 -type f -print0 2>/dev/null |
      xargs -0 -r sed -i "s#$REPO#<repo>#g; s#$WORK#<work>#g; s#/tmp/opencode#<tmp>#g; s#$HOME#<home>#g; s#$RUN_ID#<run>#g"
  fi
  exit $rc
}
trap cleanup EXIT

log "preflight"
docker image inspect "$IMG" >/dev/null 2>&1 || docker build --network host -t "$IMG" "$REPO/spike/tun/docker" >>"$EVID/run.log" 2>&1
[ -x "$GO" ] || { echo "Go toolchain not found at $GO" >&2; exit 1; }
if ip link show rl-tun0 >/dev/null 2>&1; then
  echo "host rl-tun0 exists; refusing to run" >&2
  exit 1
fi

# ---------------------------------------------------------------- build fixtures
log "building fixtures"
(
  cd "$R/dnssrv"
  CGO_ENABLED=0 "$GO" build -trimpath -o "$WORK/dnssrv" .
) >>"$EVID/run.log" 2>&1
cp "$R/socks_peek.py" "$WORK/socks_peek.py"
cp "$REPO/spike/tun/fixture/app_probe.py" "$WORK/app_probe.py"
chmod +x "$WORK/socks_peek.py" "$WORK/app_probe.py" "$WORK/dnssrv"

# self-signed CA/server cert with both test addresses and a name
openssl req -x509 -newkey rsa:2048 -nodes -days 2 \
  -keyout "$WORK/key.pem" -out "$WORK/cert.pem" \
  -subj '/CN=r16i-doh.test' \
  -addext 'subjectAltName=DNS:r16i-doh.test,IP:172.31.77.20,IP:fd00:77::20' \
  -addext 'basicConstraints=critical,CA:TRUE' >>"$EVID/run.log" 2>&1
cp "$WORK/cert.pem" "$WORK/ca.crt"

# ---------------------------------------------------------------- containers
log "creating containers/network"
docker network create --driver bridge --ipv6 \
  --subnet 172.31.77.0/24 --gateway 172.31.77.1 \
  --subnet fd00:77::/64 --gateway fd00:77::1 "$NET" >>"$EVID/run.log" 2>&1
docker run -d --name "$A" --hostname "$A" --privileged --cgroupns=private \
  --network "$NET" --ip 172.31.77.10 --ip6 fd00:77::10 \
  --dns 203.0.113.99 -v "$WORK:/work" "$IMG" sleep infinity >>"$EVID/run.log" 2>&1
docker run -d --name "$B" --hostname "$B" --cap-add=NET_ADMIN \
  --network "$NET" --ip 172.31.77.20 --ip6 fd00:77::20 \
  -v "$WORK:/work" "$IMG" sleep infinity >>"$EVID/run.log" 2>&1
sleep 1

# ---------------------------------------------------------------- peer fixtures
log "starting peer fixtures"
cap_sh_b "01-peer-setup.txt" '
set -e
ip addr add 203.0.113.53/32 dev eth0 2>/dev/null || true
ip -6 addr add 2001:db8::53/128 dev eth0 2>/dev/null || true
mkdir -p /work/logs
nohup python3 /work/socks_peek.py --listen 0.0.0.0 --port 1080 --log /work/logs/socks.log >/work/logs/socks.out 2>&1 &
nohup /work/dnssrv -mode doh -addr :8443 -cert /work/cert.pem -key /work/key.pem -log /work/logs/doh.log >/work/logs/doh.out 2>&1 &
nohup /work/dnssrv -mode tcp -addr :5353 -log /work/logs/dnstcp.log >/work/logs/dnstcp.out 2>&1 &
nohup dnsmasq --no-daemon --no-resolv --no-hosts --bind-interfaces \
  --listen-address=203.0.113.53 --listen-address=2001:db8::53 \
  --address=/canary-r16i.invalid/203.0.113.201 \
  --address=/canary-r16i.invalid/2001:db8::201 \
  --log-queries --log-facility=/work/logs/direct-dns.log >/work/logs/dnsmasq.out 2>&1 &
sleep 1
ss -lnt | grep -E ":(1080|8443|5353) " || true
ss -lun | grep -E ":(53) " || true
'

# ---------------------------------------------------------------- A setup
log "configuring client container"
cap_sh_a "02-client-setup.txt" '
set -e
mkdir -p /work/a-logs
ip route replace default via 172.31.77.20 dev eth0
ip -6 route replace default via fd00:77::20 dev eth0
cp /work/ca.crt /usr/local/share/ca-certificates/r16i.crt
update-ca-certificates >/dev/null 2>&1 || true
cat /etc/resolv.conf
sha256sum /etc/resolv.conf
'

# pinned Xray binary from the earlier disposable spike build
if [ ! -x "$WORK/xray" ]; then
  if [ -x /tmp/opencode/rl-tun-spike/bin/xray-official ]; then
    cp /tmp/opencode/rl-tun-spike/bin/xray-official "$WORK/xray"
  else
    echo "missing pinned xray binary" >&2; exit 1
  fi
fi
"$WORK/xray" version >"$EVID/00-versions.txt" 2>&1

# ---------------------------------------------------------------- config parse matrix
log "DNS transport config parse matrix (pinned Xray)"
for spec in \
  'doh|https://172.31.77.20:8443/dns-query' \
  'tcp|tcp://172.31.77.20:5353' \
  'dot|tls://172.31.77.20:853' \
  'doq|quic://172.31.77.20:853'; do
  name="${spec%%|*}"; addr="${spec#*|}"
  python3 - "$name" "$addr" >"$WORK/parse-$name.json" <<'PY'
import json, sys
name, addr = sys.argv[1], sys.argv[2]
print(json.dumps({
  "log": {"logLevel": "none"},
  "dns": {"servers": [{"address": addr, "timeoutMs": 2000}], "disableFallback": True},
  "inbounds": [{"tag": "parse-in", "protocol": "socks", "listen": "127.0.0.1", "port": 10899,
                "settings": {"auth": "noauth", "udp": False}}],
  "outbounds": [{"tag": "direct", "protocol": "freedom"}],
}, indent=2))
PY
  {
    echo "\n===== $name: $addr ====="
    set +e
    "$WORK/xray" run -test -c "$WORK/parse-$name.json" 2>&1
    echo "[exit $?]"
    set -e
  } >>"$EVID/03-config-parse.txt"
done

# helper: write a TUN config
write_tun_config() {
  local path=$1 mode=$2 upstream=$3
  python3 - "$path" "$mode" "$upstream" <<'PY'
import json, sys
path, mode, upstream = sys.argv[1], sys.argv[2], sys.argv[3]
doc = {
  "log": {"logLevel": "none", "access": "none", "error": "none"},
  "inbounds": [{
    "tag": "rl-tun-in", "protocol": "tun",
    "settings": {
      "name": "rl-tun0", "mtu": 1500,
      "gateway": ["198.18.0.1/15", "fc00::1/64"],
      "autoSystemRoutingTable": ["0.0.0.0/1", "128.0.0.0/1", "::/1", "8000::/1"],
      "autoOutboundsInterface": "auto",
      "autoSystemDnsToGateway": False
    },
    "sniffing": {"enabled": True, "destOverride": ["http", "tls"], "routeOnly": False}
  }],
  "outbounds": [
    {"tag": "rl-block", "protocol": "blackhole"},
    {"tag": "rl-exit", "protocol": "socks", "settings": {"servers": [{"address": "172.31.77.20", "port": 1080}]}}
  ],
  "routing": {"domainStrategy": "AsIs", "rules": [
    {"type": "field", "network": "tcp,udp", "port": "53", "outboundTag": ("rl-dns" if mode != "direct" else "rl-direct")},
    {"type": "field", "network": "tcp", "outboundTag": "rl-exit"},
    {"type": "field", "network": "udp", "outboundTag": "rl-block"},
    {"type": "field", "network": "tcp,udp", "outboundTag": "rl-block"}
  ]}
}
if mode == "direct":
    doc["outbounds"].append({"tag": "rl-direct", "protocol": "freedom"})
else:
    doc["outbounds"].insert(1, {"tag": "rl-dns", "protocol": "dns", "settings": {}})
    doc["dns"] = {
      "servers": [{"address": upstream, "timeoutMs": 3000}],
      "disableFallback": True,
      "queryStrategy": "UseIP"
    }
with open(path, "w") as f:
    json.dump(doc, f, indent=2)
PY
}

start_xray() {
  local cfg=$1
  docker exec "$A" sh -c "pkill -x xray 2>/dev/null || true; sleep 0.3; mkdir -p /work/a-logs; nohup /work/xray run -c '$cfg' >/work/a-logs/xray.log 2>&1 &"
  for _ in $(seq 1 60); do
    if docker exec "$A" sh -c 'ip link show rl-tun0 >/dev/null 2>&1 && ip route | grep -q "0.0.0.0/1"'; then
      return 0
    fi
    sleep 0.1
  done
  echo "xray TUN did not become ready" >&2
  docker exec "$A" sh -c 'tail -20 /work/a-logs/xray.log' >&2 || true
  return 1
}

stop_xray() {
  docker exec "$A" sh -c "pkill -x xray 2>/dev/null || true; sleep 0.5; ip link del rl-tun0 2>/dev/null || true"
}

probe_dns() {
  local server=$1 name=$2 qtype=$3 proto=${4:-}
  local args=(dns --server "$server" --name "$name" --qtype "$qtype")
  [ -n "$proto" ] && args+=("$proto")
  docker exec "$A" python3 /work/app_probe.py "${args[@]}" 2>&1 || true
}

# runtime check: tls:// is not a supported DNS scheme in this pin
python3 - "$WORK/parse-dot-run.json" >"$WORK/parse-dot-run.json" <<'PY'
import json, sys
print(json.dumps({
  "log": {"logLevel": "none"},
  "dns": {"servers": [{"address": "tls://172.31.77.20:853", "timeoutMs": 1000}], "disableFallback": True},
  "inbounds": [{"tag": "parse-in", "protocol": "socks", "listen": "127.0.0.1", "port": 10899,
                "settings": {"auth": "noauth", "udp": False}}],
  "outbounds": [{"tag": "direct", "protocol": "freedom"}],
}, indent=2))
PY
{
  echo "\n===== runtime tls:// DNS ====="
  set +e
  timeout 3 "$WORK/xray" run -c "$WORK/parse-dot-run.json" 2>&1
  echo "[exit $?]"
  set -e
} >>"$EVID/03-config-parse.txt"

# ---------------------------------------------------------------- scenario 1: direct (switch off)
log "scenario 1: phase-one direct DNS control"
write_tun_config "$WORK/xray-direct.json" direct ""
start_xray "/work/xray-direct.json"
{
  echo "=== direct A ==="; probe_dns 203.0.113.53 direct.canary-r16i.invalid A ""
  echo "=== direct AAAA ==="; probe_dns 203.0.113.53 direct.canary-r16i.invalid AAAA ""
  echo "=== direct v6 UDP ==="; probe_dns 2001:db8::53 direct.canary-r16i.invalid A ""
  echo "=== direct v6 TCP ==="; probe_dns 2001:db8::53 direct.canary-r16i.invalid A --tcp
} >"$EVID/04-direct-control.txt" 2>&1
cap_sh_a "04-direct-control.txt" 'ip -br addr show rl-tun0; ip route | grep rl-tun0; ip -6 route | grep rl-tun0'
cap_sh_b "04-direct-control.txt" 'tail -30 /work/logs/direct-dns.log'
cap_sh_a "04-direct-control.txt" 'timeout 3 getent hosts canary-r16i.invalid || echo system-dns-unavailable'
stop_xray

# ---------------------------------------------------------------- scenario 2: DoH via pool
log "scenario 2: DoH through SOCKS exit"
write_tun_config "$WORK/xray-doh.json" doh "https://172.31.77.20:8443/dns-query"
start_xray "/work/xray-doh.json"
{
  echo "=== doh A v4 query ==="; probe_dns 203.0.113.53 doh.canary-r16i.invalid A ""
  echo "=== doh AAAA v4 query ==="; probe_dns 203.0.113.53 doh.canary-r16i.invalid AAAA ""
  echo "=== doh A v6 resolver destination ==="; probe_dns 2001:db8::53 doh.canary-r16i.invalid A ""
  echo "=== doh A TCP client ==="; probe_dns 203.0.113.53 doh.canary-r16i.invalid A --tcp
} >"$EVID/05-doh-pool.txt" 2>&1
cap_sh_b "05-doh-pool.txt" 'cat /work/logs/doh.log; echo ---socks---; cat /work/logs/socks.log; echo ---direct---; tail -30 /work/logs/direct-dns.log; echo "direct_hits=$(grep -c "doh\." /work/logs/direct-dns.log 2>/dev/null || true)"'
cap_sh_a "05-doh-pool.txt" 'cat /work/a-logs/xray.log 2>/dev/null | wc -l; sha256sum /etc/resolv.conf; cat /etc/resolv.conf'
# D21 canary scan of local product-like surfaces (test fixture logs excluded)
cap_sh_a "05-doh-pool.txt" 'grep -R -n "canary-r16i" /work/a-logs /etc/resolv.conf /run 2>/dev/null | head -20; echo matches=$(grep -R -l "canary-r16i" /work/a-logs /etc/resolv.conf /run 2>/dev/null | wc -l)'
stop_xray

# ---------------------------------------------------------------- scenario 2b: DoH over IPv6 upstream
log "scenario 2b: DoH upstream over IPv6"
write_tun_config "$WORK/xray-doh6.json" doh "https://[fd00:77::20]:8443/dns-query"
start_xray "/work/xray-doh6.json"
{
  echo "=== doh6 A v4 query ==="; probe_dns 203.0.113.53 doh6.canary-r16i.invalid A ""
  echo "=== doh6 AAAA v6 resolver destination ==="; probe_dns 2001:db8::53 doh6.canary-r16i.invalid AAAA ""
} >"$EVID/05b-doh-ipv6.txt" 2>&1
cap_sh_b "05b-doh-ipv6.txt" 'cat /work/logs/doh.log; echo ---socks---; tail -10 /work/logs/socks.log; echo "direct_hits=$(grep -c "doh6\." /work/logs/direct-dns.log 2>/dev/null || true)"'
stop_xray

# ---------------------------------------------------------------- scenario 3: DNS-over-TCP via pool
log "scenario 3: DNS-over-TCP through SOCKS exit (trust boundary)"
write_tun_config "$WORK/xray-tcp.json" tcp "tcp://172.31.77.20:5353"
start_xray "/work/xray-tcp.json"
{
  echo "=== tcp A ==="; probe_dns 203.0.113.53 tcp.canary-r16i.invalid A ""
  echo "=== tcp AAAA ==="; probe_dns 203.0.113.53 tcp.canary-r16i.invalid AAAA ""
} >"$EVID/06-dns-tcp-pool.txt" 2>&1
cap_sh_b "06-dns-tcp-pool.txt" 'cat /work/logs/dnstcp.log; echo ---socks---; tail -20 /work/logs/socks.log; echo "direct_hits=$(grep -c "tcp\." /work/logs/direct-dns.log 2>/dev/null || true)"'
stop_xray

# ---------------------------------------------------------------- scenario 3b: tls:// is not DoT
log "scenario 3b: tls:// DNS is not a DoT client"
write_tun_config "$WORK/xray-dot.json" doh "tls://172.31.77.20:853"
start_xray "/work/xray-dot.json"
{
  echo "=== dot A (expected failure; no 853 listener) ==="
  timeout 10 docker exec "$A" python3 /work/app_probe.py dns --server 203.0.113.53 --name dot.canary-r16i.invalid --qtype A --timeout 3 2>&1 || true
} >"$EVID/06b-dot-negative.txt" 2>&1
cap_sh_b "06b-dot-negative.txt" 'ss -lnt | grep ":853 " || echo no-853-listener; tail -10 /work/logs/socks.log'
stop_xray

# ---------------------------------------------------------------- scenario 4: dead pool
log "scenario 4: dead SOCKS exit must fail, not fall back direct"
write_tun_config "$WORK/xray-dead.json" doh "https://172.31.77.20:8443/dns-query"
start_xray "/work/xray-dead.json"
cap_sh_b "07-dead-pool.txt" 'pkill -f "[s]ocks_peek.py" || true; sleep 0.3; ss -lnt | grep -c ":1080 " || true'
{
  echo "=== dead pool A ==="; timeout 15 docker exec "$A" python3 /work/app_probe.py dns --server 203.0.113.53 --name dead.canary-r16i.invalid --qtype A --timeout 6 2>&1 || true
  echo "=== direct resolver log (must not contain dead name) ==="
} >"$EVID/07-dead-pool.txt" 2>&1
cap_sh_b "07-dead-pool.txt" 'grep -c "canary-r16i-dead" /work/logs/direct-dns.log 2>/dev/null || true; tail -20 /work/logs/direct-dns.log; tail -20 /work/logs/doh.log'
# restart exit and prove recovery
cap_sh_b "07-dead-pool.txt" 'nohup python3 /work/socks_peek.py --listen 0.0.0.0 --port 1080 --log /work/logs/socks.log >/work/logs/socks.out 2>&1 & sleep 0.5'
{
  echo "=== recovered A ==="; probe_dns 203.0.113.53 recovered.canary-r16i.invalid A ""
} >>"$EVID/07-dead-pool.txt" 2>&1
stop_xray

# ---------------------------------------------------------------- evidence
log "collecting evidence"
cap_sh_a "00-versions.txt" 'uname -a; /work/xray version; cat /etc/os-release | head -4; ip -V'
cap_sh_b "00-versions.txt" 'uname -a; /work/xray version 2>/dev/null || true; ss -lnt | grep -E ":(1080|8443|5353) " || true'
cap "08-repro.txt" true
{
  echo "fixture source: $R/dnssrv/main.go, $R/socks_peek.py"
  echo "xray configs: $WORK/xray-*.json (copied below)"
  for f in "$WORK"/xray-*.json; do echo "--- $f"; cat "$f"; done
} >"$EVID/08-repro.txt" 2>&1

log "done"
