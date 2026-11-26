#!/usr/bin/env bash
# Reproducible R16-A TUN feasibility spike.
#
# This script creates two disposable containers on a dedicated Docker network:
#   * <prefix>-a: privileged, cgroup-namespace private, systemd as PID 1
#   * <prefix>-b: peer with TLS/HTTP/echo/UDP/DNS fixtures
# It then runs the pinned Xray TUN service under a hardened system unit and
# writes text evidence under spike/tun/evidence/.
#
# Requirements: Docker, a Go 1.27.4 toolchain, network access for the module
# cache and the pinned sing-box archive. No host DNS/firewall/route/interface
# is changed; all TUN/route/unit work happens inside the containers.
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SPIKE="$REPO/spike/tun"
WORK="${RL_TUN_WORK:-/tmp/opencode/rl-tun-spike}"
RUN_ID="${RL_TUN_RUN_ID:-$(date +%Y%m%d%H%M%S)-$$}"
A="rl-tun-spike-${RUN_ID}-a"
B="rl-tun-spike-${RUN_ID}-b"
NET="rl-tun-spike-${RUN_ID}-net"
EVID="$SPIKE/evidence"
IMG="rl-tun-spike:base"
KEEP="${RL_TUN_KEEP:-0}"

export DOCKER_CONFIG="${DOCKER_CONFIG:-$WORK/dockerconfig}"
mkdir -p "$DOCKER_CONFIG" "$WORK/bin" "$WORK/fixture" "$EVID"
GO_BIN="${GO:-}"
if [ -z "$GO_BIN" ]; then
  if command -v go >/dev/null 2>&1; then
    GO_BIN="$(command -v go)"
  elif [ -x "$REPO/.toolchain/go/bin/go" ]; then
    GO_BIN="$REPO/.toolchain/go/bin/go"
  else
    echo "Go 1.27.4 not found; set GO=/path/to/go" >&2
    exit 1
  fi
fi
export GOMODCACHE="${GOMODCACHE:-$WORK/gomodcache}"
export GOCACHE="${GOCACHE:-$WORK/gocache}"
export GOPATH="${GOPATH:-$WORK/gopath}"
export GOFLAGS="${GOFLAGS:--mod=readonly}"

sanitize_evidence() {
  [ -d "$EVID" ] || return 0
  local host
  host="$(hostname 2>/dev/null || true)"
  sed -i "s#$REPO#<repo>#g; s#$WORK#<work>#g; s#$HOME#<home>#g" "$EVID"/*.txt 2>/dev/null || true
  if [ -n "$host" ]; then
    sed -i "s#$host#<host>#g" "$EVID"/*.txt 2>/dev/null || true
  fi
  sed -i -E 's/192\.168\.1\.[0-9]+/<host-lan-ip>/g; s/192\.168\.122\.[0-9]+/<host-virbr-ip>/g; s/([0-9a-fA-F]{2}:){5}[0-9a-fA-F]{2}/<mac>/g; s/\b(wlp|enp)[a-zA-Z0-9]+\b/<host-iface>/g; s/\btailscale[0-9]*\b/<host-vpn-iface>/g' "$EVID"/*.txt 2>/dev/null || true
}

cleanup() {
  rc=$?
  sanitize_evidence
  if [ "$KEEP" = "1" ]; then
    echo "RL_TUN_KEEP=1: containers $A $B and network $NET left running" >&2
    return
  fi
  docker rm -f "$A" "$B" >/dev/null 2>&1 || true
  docker network rm "$NET" >/dev/null 2>&1 || true
  exit $rc
}
trap cleanup EXIT

log() { printf '\n===== %s =====\n' "$*" >>"$EVID/run.log"; }
cap() {
  local f=$1; shift
  [[ $f = /* ]] || f="$EVID/$f"
  { printf '\n$ %s\n' "$*"; "$@" 2>&1; } >>"$f"
}
cap_try() {
  local f=$1; shift
  [[ $f = /* ]] || f="$EVID/$f"
  { printf '\n$ %s\n' "$*"; set +e; "$@" 2>&1; rc=$?; set -e; printf '[exit %d]\n' "$rc"; } >>"$f"
}
cap_sh() {
  local f=$1 c=$2 s=$3
  [[ $f = /* ]] || f="$EVID/$f"
  { printf '\n$ docker exec %s sh -c %q\n' "$c" "$s"; docker exec "$c" sh -c "$s" 2>&1 || true; } >>"$f"
}

rm -f "$EVID"/*.txt
: >"$EVID/run.log"
log "R16-A run $(date -u +%FT%TZ) containers=$A,$B network=$NET"

# ---------------------------------------------------------------- build image
if ! docker image inspect "$IMG" >/dev/null 2>&1; then
  log "building $IMG"
  docker build --network host -t "$IMG" "$SPIKE/docker" >>"$EVID/run.log" 2>&1
fi

# ---------------------------------------------------------------- build binaries
log "building spike binaries"
(
  cd "$SPIKE/src"
  "$GO_BIN" build -o "$WORK/bin/tun-service" ./tunservice
  "$GO_BIN" build -o "$WORK/bin/egressprobe" ./egressprobe
  "$GO_BIN" build -o "$WORK/bin/xray-official" github.com/xtls/xray-core/main
) >>"$EVID/run.log" 2>&1

# ---------------------------------------------------------------- sing-box pin
SINGBOX="$WORK/bin/sing-box"
SINGBOX_SHA=699a1b510aa4396b2176d32d1181920399ebf1e267835da6f3b598d0f7c24968
HY2_PASS="$(openssl rand -hex 16)"
if [ ! -x "$SINGBOX" ]; then
  curl -fsSL -o "$WORK/sing-box.tar.gz" \
    https://github.com/SagerNet/sing-box/releases/download/v1.14.4/sing-box-1.14.4-linux-amd64.tar.gz
  echo "$SINGBOX_SHA  $WORK/sing-box.tar.gz" | sha256sum -c - >>"$EVID/run.log"
  tar -xzf "$WORK/sing-box.tar.gz" -C "$WORK"
  cp "$WORK/sing-box-1.14.4-linux-amd64/sing-box" "$SINGBOX"
fi

# ---------------------------------------------------------------- certificates
log "generating per-run fixture certificate"
openssl req -x509 -newkey rsa:2048 -nodes \
  -keyout "$WORK/fixture/key.pem" -out "$WORK/fixture/cert.pem" -days 2 \
  -subj '/CN=canary-r16a.invalid' \
  -addext 'subjectAltName=DNS:canary-r16a.invalid,DNS:canary6-r16a.invalid,DNS:canary-http-r16a.invalid,IP:172.31.77.20,IP:fd00:77::20' \
  >>"$EVID/run.log" 2>&1

# ---------------------------------------------------------------- containers
log "creating network and containers"
docker network create --driver bridge --ipv6 \
  --subnet 172.31.77.0/24 --subnet fd00:77::/64 "$NET" >>"$EVID/run.log"
docker run -d --name "$A" --hostname "$A" --privileged --cgroupns=private \
  --tmpfs /run --tmpfs /tmp --network "$NET" --ip 172.31.77.10 --ip6 fd00:77::10 \
  "$IMG" >>"$EVID/run.log"
docker run -d --name "$B" --hostname "$B" --network "$NET" --cap-add=NET_ADMIN \
  --ip 172.31.77.20 --ip6 fd00:77::20 --entrypoint /bin/bash "$IMG" -c 'sleep infinity' \
  >>"$EVID/run.log"

for _ in $(seq 1 60); do
  s="$(docker exec "$A" systemctl is-system-running 2>/dev/null || true)"
  case "$s" in running|degraded) break;; esac
  sleep 1
done
docker exec "$A" test -S /run/systemd/private

# ---------------------------------------------------------------- versions
V="$EVID/00-versions.txt"
: >"$V"
cap "$V" docker version --format 'docker server {{.Server.Version}}'
cap "$V" docker image inspect "$IMG" --format 'image id {{.Id}} created {{.Created}}'
cap "$V" docker image inspect ubuntu:26.04 --format 'base id {{.Id}}'
cap "$V" uname -a
cap "$V" "$GO_BIN" version
cap "$V" "$WORK/bin/xray-official" version
cap_sh "$V" "$A" 'systemctl --version | head -2; ip -V; cat /etc/os-release | grep -E "^(PRETTY_NAME|VERSION_ID)="; cat /proc/sys/kernel/osrelease'
cap_sh "$V" "$A" 'cat /proc/self/cgroup; lsns -t cgroup -o NS,TYPE,NPROCS,PID,COMMAND | head'
cap "$V" docker inspect "$A" --format 'cgroupns={{.HostConfig.CgroupnsMode}} privileged={{.HostConfig.Privileged}}'
cap "$V" grep -n 'xray-core' "$SPIKE/src/go.mod"
cap "$V" "$SINGBOX" version

# ---------------------------------------------------------------- peer fixtures
log "installing peer fixtures"
docker exec "$B" mkdir -p /opt/fixture
for f in peer_server.py socks_fixture.py app_probe.py tunctl.py; do
  docker cp "$SPIKE/fixture/$f" "$B:/opt/$f"
done
docker cp "$WORK/fixture/cert.pem" "$B:/opt/cert.pem"
docker cp "$WORK/fixture/key.pem" "$B:/opt/key.pem"
docker cp "$SINGBOX" "$B:/opt/sing-box"
docker exec -d "$B" sh -c 'exec python3 /opt/peer_server.py --cert /opt/cert.pem --key /opt/key.pem >/opt/fixture/peer.out 2>&1'
docker exec -d "$B" sh -c 'exec dnsmasq --no-daemon --no-resolv --no-hosts --bind-interfaces --listen-address=172.31.77.20 --listen-address=fd00:77::20 --address=/canary-r16a.invalid/203.0.113.201 --address=/canary-r16a.invalid/2001:db8::201 --address=/canary6-r16a.invalid/2001:db8::201 --log-queries --log-facility=/opt/fixture/dns.log >/opt/fixture/dnsmasq.out 2>&1'
for _ in $(seq 1 30); do
  docker exec "$B" sh -c 'ss -lnt | grep -q ":7101 " && ss -lun | grep -q ":9999 "' 2>/dev/null && break
  sleep 0.2
done

# ---------------------------------------------------------------- primary setup
log "installing service"
docker exec "$A" sh -c 'id rlspike >/dev/null 2>&1 || useradd -u 1500 -M -s /usr/sbin/nologin rlspike; systemctl disable --now dnsmasq >/dev/null 2>&1 || true; install -d -m 0755 /usr/libexec/routelatch-spike /etc/routelatch-tun-spike; mkdir -p /opt/fixture'
for f in app_probe.py socks_fixture.py tunctl.py; do
  docker cp "$SPIKE/fixture/$f" "$A:/opt/$f"
done
docker cp "$WORK/bin/tun-service" "$A:/opt/tun-service"
docker cp "$WORK/bin/egressprobe" "$A:/opt/egressprobe"
docker cp "$WORK/bin/xray-official" "$A:/opt/xray-official"
docker cp "$WORK/fixture/cert.pem" "$A:/opt/cert.pem"
docker cp "$SPIKE/xray-tun.json" "$A:/etc/routelatch-tun-spike/xray.json"
docker cp "$SPIKE/service.json" "$A:/etc/routelatch-tun-spike/service.json"
docker cp "$SPIKE/units/routelatch-tun-spike.service" "$A:/etc/systemd/system/routelatch-tun-spike.service"
docker exec "$A" sh -c 'install -m 0755 /opt/tun-service /usr/libexec/routelatch-spike/tun-service; chown root:root /usr/libexec/routelatch-spike/tun-service /etc/routelatch-tun-spike/xray.json /etc/routelatch-tun-spike/service.json /etc/systemd/system/routelatch-tun-spike.service; chmod 0644 /etc/routelatch-tun-spike/*.json /etc/systemd/system/routelatch-tun-spike.service; systemctl daemon-reload'
docker exec -d "$A" sh -c 'exec python3 /opt/socks_fixture.py --peer4 172.31.77.20 --peer6 fd00:77::20 --log /opt/socks.log >/opt/fixture-socks.out 2>&1'
for _ in $(seq 1 30); do
  docker exec "$A" sh -c 'ss -lnt | grep -q ":1080 "' 2>/dev/null && break
  sleep 0.2
done
docker exec "$A" sha256sum /etc/resolv.conf >"$WORK/resolv.before"

# ---------------------------------------------------------------- unit / caps
log "unit and capability evidence"
V="$EVID/01-systemd-unit.txt"
: >"$V"
cap "$V" docker exec "$A" stat -c '%U %G %a %n' /usr/libexec/routelatch-spike/tun-service /etc/routelatch-tun-spike/xray.json /etc/routelatch-tun-spike/service.json /etc/systemd/system/routelatch-tun-spike.service
cap "$V" docker exec "$A" systemctl show routelatch-tun-spike.service -p User -p Group -p AmbientCapabilities -p CapabilityBoundingSet -p NoNewPrivileges -p ProtectSystem -p ProtectHome -p PrivateTmp -p PrivateDevices -p ProtectKernelTunables -p ProtectKernelModules -p ProtectControlGroups -p RestrictAddressFamilies -p RuntimeDirectory -p RuntimeDirectoryMode -p RuntimeDirectoryPreserve
cap "$V" docker exec "$A" sh -c 'systemctl start routelatch-tun-spike.service; sleep 1; P=$(systemctl show -p MainPID --value routelatch-tun-spike.service); echo mainpid=$P; readlink /proc/$P/exe; grep -E "^(Uid|Gid|CapEff|CapBnd|NoNewPrivs):" /proc/$P/status; ls -l /run/routelatch-tun/ /run/routelatch-tun/control.sock'
cap "$V" docker exec "$A" journalctl -u routelatch-tun-spike.service --no-pager -o cat
cap "$V" docker exec "$A" systemd-analyze security routelatch-tun-spike.service --no-pager 2>/dev/null || true
cap_try "$V" docker exec "$A" python3 /opt/tunctl.py status
cap "$V" docker exec "$A" runuser -u rlspike -- python3 /opt/tunctl.py status

# ---------------------------------------------------------------- enable
log "enabling TUN"
V="$EVID/03-tun-routes.txt"
: >"$V"
cap "$V" docker exec "$A" runuser -u rlspike -- python3 /opt/tunctl.py enable
cap "$V" docker exec "$A" sh -c 'ip -br link; echo ---; ip route; echo ---; ip -6 route; echo ---; ip -br addr show rl-tun0; echo ---; cat /run/routelatch-tun/lease.json; echo; stat -c "%a %U %G %n" /run/routelatch-tun/control.sock'
cap "$V" docker exec "$A" ip route get 203.0.113.201
cap "$V" docker exec "$A" ip -6 route get 2001:db8::201

# Test-only /32 and /128 routes force the on-link peer into the TUN. In a real
# setup the peer is an off-link internet destination captured by the halves.
docker exec "$A" sh -c 'ip route add 172.31.77.20/32 dev rl-tun0; ip -6 route add fd00:77::20/128 dev rl-tun0'
cap "$V" docker exec "$A" sh -c 'ip route get 172.31.77.20; ip -6 route get fd00:77::20'

# ---------------------------------------------------------------- app TCP
log "TCP/HTTP/TLS through TUN"
V="$EVID/04-app-tcp-sniff.txt"
: >"$V"
docker exec "$A" curl -s http://127.0.0.1:18080/reset >/dev/null
cap_try "$V" docker exec "$A" runuser -u rlspike -- python3 /opt/app_probe.py tcp --host 172.31.77.20 --port 7102 --mode echo
cap_try "$V" docker exec "$A" runuser -u rlspike -- python3 /opt/app_probe.py tcp --host 172.31.77.20 --port 7101 --mode tls --sni canary-r16a.invalid
cap_try "$V" docker exec "$A" runuser -u rlspike -- python3 /opt/app_probe.py tcp --host fd00:77::20 --port 7101 --mode tls --sni canary6-r16a.invalid
cap_try "$V" docker exec "$A" runuser -u rlspike -- python3 /opt/app_probe.py tcp --host 172.31.77.20 --port 7104 --mode http
cap_sh "$V" "$A" 'curl -s http://127.0.0.1:18080/counters; echo; tail -12 /opt/socks.log'
cap_sh "$V" "$B" 'tail -12 /opt/fixture/peer.log'

# ---------------------------------------------------------------- DNS / UDP
log "DNS and UDP"
V="$EVID/05-dns-udp.txt"
: >"$V"
cap_try "$V" docker exec "$A" runuser -u rlspike -- python3 /opt/app_probe.py dns --server 172.31.77.20 --name canary-r16a.invalid --qtype A
cap_try "$V" docker exec "$A" runuser -u rlspike -- python3 /opt/app_probe.py dns --server 172.31.77.20 --name canary-r16a.invalid --qtype AAAA
cap_try "$V" docker exec "$A" runuser -u rlspike -- python3 /opt/app_probe.py dns --server 172.31.77.20 --name canary-r16a.invalid --qtype A --tcp
cap_try "$V" docker exec "$A" runuser -u rlspike -- python3 /opt/app_probe.py udp --host 172.31.77.20 --port 9999 --payload RLSPIKE-UDP-NON53
cap_try "$V" docker exec "$A" runuser -u rlspike -- python3 /opt/app_probe.py udp --host 172.31.77.20 --port 443 --payload RLSPIKE-QUIC-INITIAL
sleep 1
cap_sh "$V" "$B" 'tail -12 /opt/fixture/dns.log; echo ---udp---; cat /opt/fixture/udp.log'
cap_sh "$V" "$A" 'grep -n "autoSystemDnsToGateway" /etc/routelatch-tun-spike/xray.json; sha256sum /etc/resolv.conf; cat /etc/resolv.conf'
docker exec "$A" sha256sum /etc/resolv.conf >"$WORK/resolv.after"
cap "$V" diff -u "$WORK/resolv.before" "$WORK/resolv.after" || true
cap_sh "$V" "$A" 'command -v resolvectl || echo resolvectl-absent; ls /run/systemd/resolve 2>/dev/null || echo systemd-resolved-not-used'

# ---------------------------------------------------------------- autoOutboundsInterface
log "autoOutboundsInterface only (no explicit DNS interface)"
V="$EVID/06a-auto-outbounds.txt"
: >"$V"
docker exec "$A" sh -c 'cp /etc/routelatch-tun-spike/xray.json /etc/routelatch-tun-spike/xray.standard.json'
python3 - "$SPIKE/xray-tun.json" "$WORK/fixture/xray-auto.json" <<'PY'
import json, sys
c = json.load(open(sys.argv[1]))
for o in c["outbounds"]:
    if o["tag"] == "rl-tun-dns":
        o.pop("streamSettings", None)
json.dump(c, open(sys.argv[2], "w"), indent=2)
PY
docker exec "$A" runuser -u rlspike -- python3 /opt/tunctl.py disable >/dev/null
docker exec "$A" systemctl stop routelatch-tun-spike.service
docker cp "$WORK/fixture/xray-auto.json" "$A:/etc/routelatch-tun-spike/xray.json"
docker exec "$A" sh -c 'systemctl start routelatch-tun-spike.service; sleep 1; runuser -u rlspike -- python3 /opt/tunctl.py enable; ip route add 172.31.77.20/32 dev rl-tun0'
cap_try "$V" docker exec "$A" runuser -u rlspike -- python3 /opt/app_probe.py dns --server 172.31.77.20 --name canary-r16a.invalid
cap_sh "$V" "$B" 'tail -4 /opt/fixture/dns.log'
docker exec "$A" sh -c 'cp /etc/routelatch-tun-spike/xray.standard.json /etc/routelatch-tun-spike/xray.json; systemctl restart routelatch-tun-spike.service; sleep 1; ip route add 172.31.77.20/32 dev rl-tun0 2>/dev/null || true; ip -6 route add fd00:77::20/128 dev rl-tun0 2>/dev/null || true'
cap "$V" docker exec "$A" runuser -u rlspike -- python3 /opt/tunctl.py status

# ---------------------------------------------------------------- engine egress
log "engine egress bypass"
V="$EVID/06-engine-egress.txt"
: >"$V"
for mode in direct xray-bound xray-loop; do
  docker exec "$A" curl -s http://127.0.0.1:18080/reset >/dev/null
  cap_try "$V" docker exec "$A" runuser -u rlspike -- /opt/egressprobe -mode "$mode" -interface eth0 -target 172.31.77.20:7103 -timeout 5s
  cap_sh "$V" "$A" 'curl -s http://127.0.0.1:18080/counters; echo'
done

# ---------------------------------------------------------------- sidecar
log "sing-box sidecar"
V="$EVID/07-singbox-sidecar.txt"
: >"$V"
cap "$V" "$SINGBOX" version
docker cp "$SINGBOX" "$A:/opt/sing-box"
cap_sh "$V" "$A" '/opt/sing-box version | head -1'
cat >"$WORK/fixture/singbox-direct.json" <<'JSON'
{
  "log": { "level": "error", "output": "/opt/fixture/singbox-direct.log" },
  "inbounds": [ { "type": "mixed", "tag": "in", "listen": "127.0.0.1", "listen_port": 1081 } ],
  "outbounds": [ { "type": "direct", "tag": "direct" } ],
  "route": { "default_interface": "eth0", "final": "direct" }
}
JSON
cat >"$WORK/fixture/singbox-loop.json" <<'JSON'
{
  "log": { "level": "error", "output": "/opt/fixture/singbox-loop.log" },
  "inbounds": [ { "type": "mixed", "tag": "in", "listen": "127.0.0.1", "listen_port": 1082 } ],
  "outbounds": [ { "type": "direct", "tag": "direct" } ],
  "route": { "final": "direct" }
}
JSON
docker cp "$WORK/fixture/singbox-direct.json" "$A:/opt/singbox-direct.json"
docker cp "$WORK/fixture/singbox-loop.json" "$A:/opt/singbox-loop.json"
cap_sh "$V" "$A" '/opt/sing-box check -c /opt/singbox-direct.json; echo direct_check=$?; /opt/sing-box check -c /opt/singbox-loop.json; echo loop_check=$?'
cap_sh "$V" "$A" 'setsid /opt/sing-box run -c /opt/singbox-direct.json </dev/null >/opt/fixture/singbox-direct.out 2>&1 & sleep 1; curl -s http://127.0.0.1:18080/reset >/dev/null; curl -s -m 5 --socks5-hostname 127.0.0.1:1081 http://172.31.77.20:7104/; echo; curl -s http://127.0.0.1:18080/counters; echo'
cap_sh "$V" "$A" 'setsid /opt/sing-box run -c /opt/singbox-loop.json </dev/null >/opt/fixture/singbox-loop.out 2>&1 & sleep 1; curl -s http://127.0.0.1:18080/reset >/dev/null; curl -s -m 5 --socks5-hostname 127.0.0.1:1082 http://172.31.77.20:7104/; echo; curl -s http://127.0.0.1:18080/counters; echo'

# hysteria2 runtime: server on peer, client on primary.
cat >"$WORK/fixture/singbox-hy2-server.json" <<'JSON'
{
  "log": { "level": "info", "output": "/opt/fixture/hy2-server.log" },
  "inbounds": [ { "type": "hysteria2", "tag": "hy2-in", "listen": "0.0.0.0", "listen_port": 8443,
    "users": [ { "name": "spike", "password": "__HY2_PASS__" } ],
    "tls": { "enabled": true, "certificate_path": "/opt/cert.pem", "key_path": "/opt/key.pem" } } ],
  "outbounds": [ { "type": "direct", "tag": "direct" } ]
}
JSON
cat >"$WORK/fixture/singbox-hy2-client.json" <<'JSON'
{
  "log": { "level": "info", "output": "/opt/fixture/hy2-client.log" },
  "inbounds": [ { "type": "mixed", "tag": "in", "listen": "127.0.0.1", "listen_port": 1083 } ],
  "outbounds": [ { "type": "hysteria2", "tag": "hy2", "server": "172.31.77.20", "server_port": 8443,
    "password": "__HY2_PASS__",
    "tls": { "enabled": true, "server_name": "canary-r16a.invalid", "certificate_path": "/opt/cert.pem" } } ],
  "route": { "default_interface": "eth0", "final": "hy2" }
}
JSON
cat >"$WORK/fixture/singbox-hy2-client-loop.json" <<'JSON'
{
  "log": { "level": "debug", "output": "/opt/fixture/hy2-client-loop.log" },
  "inbounds": [ { "type": "mixed", "tag": "in", "listen": "127.0.0.1", "listen_port": 1084 } ],
  "outbounds": [ { "type": "hysteria2", "tag": "hy2", "server": "172.31.77.20", "server_port": 8443,
    "password": "__HY2_PASS__",
    "tls": { "enabled": true, "server_name": "canary-r16a.invalid", "certificate_path": "/opt/cert.pem" } } ],
  "route": { "final": "hy2" }
}
JSON
sed -i "s/__HY2_PASS__/$HY2_PASS/" "$WORK/fixture/singbox-hy2-server.json" "$WORK/fixture/singbox-hy2-client.json" "$WORK/fixture/singbox-hy2-client-loop.json"
docker cp "$WORK/fixture/singbox-hy2-server.json" "$B:/opt/singbox-hy2-server.json"
docker cp "$WORK/fixture/singbox-hy2-client.json" "$A:/opt/singbox-hy2-client.json"
docker cp "$WORK/fixture/singbox-hy2-client-loop.json" "$A:/opt/singbox-hy2-client-loop.json"
cap_sh "$V" "$B" '/opt/sing-box check -c /opt/singbox-hy2-server.json; echo server_check=$?'
cap_sh "$V" "$A" '/opt/sing-box check -c /opt/singbox-hy2-client.json; echo client_check=$?'
docker exec -d "$B" sh -c 'exec /opt/sing-box run -c /opt/singbox-hy2-server.json >/opt/fixture/hy2-server.out 2>&1'
sleep 2
# Baseline: /32 test route removed, so the peer is on-link and hysteria2 works
# regardless of the binding. This validates the fixture.
docker exec "$A" sh -c 'ip route del 172.31.77.20/32 dev rl-tun0 2>/dev/null || true'
docker exec -d "$A" sh -c 'exec /opt/sing-box run -c /opt/singbox-hy2-client.json >/opt/fixture/hy2-client.out 2>&1'
sleep 2
cap_sh "$V" "$A" 'curl -s http://127.0.0.1:18080/reset >/dev/null; out=$(curl -s -m 10 --socks5-hostname 127.0.0.1:1083 http://172.31.77.20:7104/); echo "baseline body=$out rc=$?"; curl -s http://127.0.0.1:18080/counters; echo'
# Force the peer through TUN and restart the hysteria2 client.
docker exec "$A" sh -c 'for p in $(pgrep -f "^/opt/sing-box run -c /opt/singbox-hy2-client.json"); do kill $p; done; ip route add 172.31.77.20/32 dev rl-tun0'
docker exec -d "$A" sh -c 'exec /opt/sing-box run -c /opt/singbox-hy2-client.json >/opt/fixture/hy2-client.out 2>&1'
sleep 2
cap_sh "$V" "$A" 'curl -s http://127.0.0.1:18080/reset >/dev/null; out=$(curl -s -m 10 --socks5-hostname 127.0.0.1:1083 http://172.31.77.20:7104/); echo "tun+default_interface body=$out rc=$?"; curl -s http://127.0.0.1:18080/counters; echo'
# Negative control: no default_interface under TUN.
docker exec "$A" sh -c 'for p in $(pgrep -f "^/opt/sing-box run -c /opt/singbox-hy2-client.json"); do kill $p; done'
docker exec -d "$A" sh -c 'exec /opt/sing-box run -c /opt/singbox-hy2-client-loop.json >/opt/fixture/hy2-client-loop.out 2>&1'
sleep 3
cap_sh "$V" "$A" 'curl -s http://127.0.0.1:18080/reset >/dev/null; out=$(curl -s -m 8 --socks5-hostname 127.0.0.1:1084 http://172.31.77.20:7104/); echo "no-default_interface body=$out rc=$?"; curl -s http://127.0.0.1:18080/counters; echo; tail -8 /opt/fixture/hy2-client-loop.log'

# ---------------------------------------------------------------- fail closed
log "fail-closed with engine gone"
V="$EVID/08-fail-closed.txt"
: >"$V"
cap_try "$V" docker exec "$A" runuser -u rlspike -- python3 /opt/app_probe.py tcp --host 172.31.77.20 --port 7102 --mode echo
BEFORE=$(docker exec "$B" grep -c '"event": "echo"' /opt/fixture/peer.log || true)
docker exec "$A" sh -c 'P=$(pgrep -f "^python3 /opt/socks_fixture.py"); echo fixture_pid=$P; kill -9 $P; echo killed'
cap_sh "$V" "$A" 'ip link show rl-tun0 | head -1; ip route | grep -E "0.0.0.0/1|128.0.0.0/1"'
cap_try "$V" docker exec "$A" runuser -u rlspike -- python3 /opt/app_probe.py tcp --host 172.31.77.20 --port 7102 --mode echo
AFTER=$(docker exec "$B" grep -c '"event": "echo"' /opt/fixture/peer.log || true)
printf 'peer echo requests before kill=%s after failed probe=%s\n' "$BEFORE" "$AFTER" >>"$V"
docker exec -d "$A" sh -c 'exec python3 /opt/socks_fixture.py --peer4 172.31.77.20 --peer6 fd00:77::20 --log /opt/socks.log >/opt/fixture-socks.out 2>&1'
sleep 1
cap_try "$V" docker exec "$A" runuser -u rlspike -- python3 /opt/app_probe.py tcp --host 172.31.77.20 --port 7102 --mode echo

# ---------------------------------------------------------------- restart lease
log "service restart with lease"
V="$EVID/09-restart-lease.txt"
: >"$V"
docker exec "$A" sh -c 'ip route add 172.31.77.20/32 dev rl-tun0 2>/dev/null || true'
cap_sh "$V" "$A" 'rm -f /tmp/gap; ( i=0; while [ $i -lt 500 ]; do printf "%s %s\n" "$(date +%s%N)" "$([ -d /sys/class/net/rl-tun0 ] && echo 1 || echo 0)"; i=$((i+1)); sleep 0.005; done ) >/tmp/gap & MON=$!; systemctl restart routelatch-tun-spike.service; wait $MON; awk "NR>1 && \$2!=prev {print \"gap_ms=\" int((\$1-prevt)/1000000), \"from=\" prev, \"to=\" \$2}; {prev=\$2; prevt=\$1}" /tmp/gap'
cap "$V" docker exec "$A" runuser -u rlspike -- python3 /opt/tunctl.py status
docker exec "$A" sh -c 'ip route add 172.31.77.20/32 dev rl-tun0 2>/dev/null || true'
cap_try "$V" docker exec "$A" runuser -u rlspike -- python3 /opt/app_probe.py tcp --host 172.31.77.20 --port 7102 --mode echo
cap "$V" docker exec "$A" cat /run/routelatch-tun/lease.json

# ---------------------------------------------------------------- SIGKILL
log "SIGKILL fail-open window"
V="$EVID/10-sigkill-failopen.txt"
: >"$V"
cap_sh "$V" "$A" 'T0=$(date +%s%N); systemctl kill -s KILL routelatch-tun-spike.service; while [ -d /sys/class/net/rl-tun0 ]; do sleep 0.005; done; T1=$(date +%s%N); while [ ! -d /sys/class/net/rl-tun0 ]; do sleep 0.005; done; T2=$(date +%s%N); echo "kill_to_link_down_ms=$(( (T1-T0)/1000000 ))"; echo "link_down_to_up_ms=$(( (T2-T1)/1000000 ))"; echo "kill_to_recovered_ms=$(( (T2-T0)/1000000 ))"; echo "active=$(systemctl is-active routelatch-tun-spike.service)"; runuser -u rlspike -- python3 /opt/tunctl.py status'
docker exec "$A" sh -c 'ip route add 172.31.77.20/32 dev rl-tun0 2>/dev/null || true'
cap_try "$V" docker exec "$A" runuser -u rlspike -- python3 /opt/app_probe.py tcp --host 172.31.77.20 --port 7102 --mode echo
cap_try "$V" docker exec "$A" runuser -u rlspike -- python3 /opt/app_probe.py dns --server 172.31.77.20 --name canary-r16a.invalid
cap "$V" docker exec "$A" cat /run/routelatch-tun/lease.json

echo "--- orphan test with Restart disabled (drop-in), then restore the real unit" >>"$V"
cap_sh "$V" "$A" 'mkdir -p /run/systemd/system/routelatch-tun-spike.service.d; printf "[Service]\nRestart=no\n" >/run/systemd/system/routelatch-tun-spike.service.d/restart.conf; systemctl daemon-reload; systemctl restart routelatch-tun-spike.service; sleep 1; ip route add 172.31.77.20/32 dev rl-tun0 2>/dev/null || true; T0=$(date +%s%N); systemctl kill -s KILL routelatch-tun-spike.service; while [ -d /sys/class/net/rl-tun0 ]; do sleep 0.005; done; T1=$(date +%s%N); echo "orphan_link_gone_ms=$(( (T1-T0)/1000000 ))"; sleep 2; echo "active=$(systemctl is-active routelatch-tun-spike.service)"; ip link show rl-tun0 2>/dev/null || echo link_absent; ip route | grep rl-tun0 || echo no_v4_tun_route; ip -6 route | grep rl-tun0 || echo no_v6_tun_route; cat /run/routelatch-tun/lease.json'
cap_try "$V" docker exec "$A" runuser -u rlspike -- python3 /opt/app_probe.py tcp --host 172.31.77.20 --port 7102 --mode echo
cap_try "$V" docker exec "$A" runuser -u rlspike -- python3 /opt/app_probe.py dns --server 172.31.77.20 --name canary-r16a.invalid
cap_sh "$V" "$A" 'rm -f /run/systemd/system/routelatch-tun-spike.service.d/restart.conf; rmdir /run/systemd/system/routelatch-tun-spike.service.d 2>/dev/null || true; systemctl daemon-reload; systemctl start routelatch-tun-spike.service; sleep 1; ip route add 172.31.77.20/32 dev rl-tun0 2>/dev/null || true; ip link show rl-tun0 | head -1; runuser -u rlspike -- python3 /opt/tunctl.py status'
cap_try "$V" docker exec "$A" runuser -u rlspike -- python3 /opt/app_probe.py tcp --host 172.31.77.20 --port 7102 --mode echo

# ---------------------------------------------------------------- tun off
log "tun off and stop cleanup"
V="$EVID/11-tun-off-cleanup.txt"
: >"$V"
cap "$V" docker exec "$A" runuser -u rlspike -- python3 /opt/tunctl.py disable
cap_sh "$V" "$A" 'ip link show rl-tun0 2>/dev/null || echo link_absent; ip route | grep rl-tun0 || echo no_v4_tun_route; ip -6 route | grep rl-tun0 || echo no_v6_tun_route; cat /run/routelatch-tun/lease.json'
cap "$V" docker exec "$A" systemctl stop routelatch-tun-spike.service
cap_sh "$V" "$A" 'systemctl is-active routelatch-tun-spike.service || true; ip link show rl-tun0 2>/dev/null || echo link_absent_after_stop'

# ---------------------------------------------------------------- collision
log "constants and collision"
V="$EVID/12-collision.txt"
: >"$V"
cap_sh "$V" "$A" 'echo "--- container before"; ip -br link; ip route; ip -6 route; echo "--- host read-only (not modified)"; true'
cap "$V" sh -c 'echo "--- host read-only"; ip -br link; echo ---; ip route; echo ---; ip -6 route'
cap_sh "$V" "$A" 'ip route add 192.168.50.0/24 dev eth0; ip tuntap add dev vpn0 mode tun; ip link set vpn0 up; ip addr add 10.8.0.1/24 dev vpn0; ip -6 addr add fd42:0:0:1::1/64 dev vpn0 2>/dev/null || true; echo "--- simulated LAN/VPN"; ip -br link | grep -E "rl-tun0|vpn0"; ip route | grep -E "198\.18|10\.8\.|192\.168\.50"; ip -6 route | grep -E "fc00::/64|fd42"'
docker exec "$A" sh -c 'systemctl start routelatch-tun-spike.service; sleep 1; runuser -u rlspike -- python3 /opt/tunctl.py enable >/dev/null'
cap_sh "$V" "$A" 'echo "--- after TUN"; ip -br addr show rl-tun0; ip route | grep -E "rl-tun0|192\.168\.50|10\.8\."; ip -6 route | grep -E "rl-tun0|fd42"'
# LAN direct check: reach a second address on the peer via eth0, not TUN.
docker exec "$B" ip addr add 192.168.50.20/24 dev eth0
cap_try "$V" docker exec "$A" runuser -u rlspike -- python3 /opt/app_probe.py tcp --host 192.168.50.20 --port 7104 --mode http
docker exec "$B" ip addr del 192.168.50.20/24 dev eth0
docker exec "$A" sh -c 'runuser -u rlspike -- python3 /opt/tunctl.py disable >/dev/null; systemctl stop routelatch-tun-spike.service; ip link del vpn0; ip route del 192.168.50.0/24 dev eth0 2>/dev/null || true'

# ---------------------------------------------------------------- D21
log "D21 canary scan"
V="$EVID/13-d21-canary.txt"
: >"$V"
docker exec "$A" sh -c 'systemctl start routelatch-tun-spike.service; sleep 1; runuser -u rlspike -- python3 /opt/tunctl.py enable >/dev/null; ip route add 172.31.77.20/32 dev rl-tun0; ip -6 route add fd00:77::20/128 dev rl-tun0'
cap_try "$V" docker exec "$A" runuser -u rlspike -- python3 /opt/app_probe.py tcp --host 172.31.77.20 --port 7101 --mode tls --sni canary-r16a.invalid
cap_try "$V" docker exec "$A" runuser -u rlspike -- python3 /opt/app_probe.py tcp --host 172.31.77.20 --port 7104 --mode http
cap_try "$V" docker exec "$A" runuser -u rlspike -- python3 /opt/app_probe.py dns --server 172.31.77.20 --name canary-r16a.invalid
docker exec "$A" runuser -u rlspike -- python3 /opt/tunctl.py status >"$WORK/status.json" 2>&1 || true
cap "$V" cat "$WORK/status.json"
docker exec "$A" journalctl -u routelatch-tun-spike.service --no-pager >"$WORK/journal.txt"
{
  echo "--- canary in service journal:"
  grep -In "canary-r16a\|canary6-r16a\|canary-http-r16a\|203.0.113.201\|2001:db8::201" \
    "$WORK/journal.txt" "$WORK/status.json" || echo NO_CANARY_IN_SERVICE_JOURNAL_OR_STATUS
  echo "--- canary in lease/runtime/config:"
  docker exec "$A" sh -c 'grep -RIn "canary-r16a\|canary6-r16a\|canary-http-r16a\|203.0.113.201\|2001:db8::201" /run/routelatch-tun /etc/routelatch-tun-spike 2>/dev/null || echo NO_CANARY_IN_SERVICE_STATE_CONFIG'
  echo "--- fixture log (allowed to log destinations):"
  docker exec "$A" tail -3 /opt/socks.log
  echo "--- service journal peer-address count (informational):"
  grep -c "172.31.77.20\|fd00:77::20" "$WORK/journal.txt" || true
} >>"$V"
MOD="$(ls -d "$GOMODCACHE"/github.com/xtls/xray-core@* 2>/dev/null | head -1 || true)"
{
  echo '$ grep -n "logLevel" spike/tun/xray-tun.json'
  grep -n "logLevel" "$SPIKE/xray-tun.json"
  echo
  if [ -n "$MOD" ]; then
    echo "$ grep -n \"processing from\" <module>/proxy/tun/*.go"
    grep -n "processing from" "$MOD"/proxy/tun/*.go
    echo
    echo "$ sed -n \"240,250p\" <module>/proxy/tun/handler.go"
    sed -n "240,250p" "$MOD"/proxy/tun/handler.go
  fi
} >"$EVID/13b-d21-source-config.txt"
# The fixture logs are test-only and may contain the canary; service surfaces are clean.
docker exec "$A" sh -c 'runuser -u rlspike -- python3 /opt/tunctl.py disable >/dev/null; systemctl stop routelatch-tun-spike.service'

# ---------------------------------------------------------------- cleanup
log "cleanup checks"
V="$EVID/14-cleanup.txt"
: >"$V"
cap_sh "$V" "$A" 'systemctl is-active routelatch-tun-spike.service || true; ip link show rl-tun0 2>/dev/null || echo link_absent; ip route | grep -E "rl-tun0|198\.18" || echo no_tun_routes; ip -6 route | grep -E "rl-tun0|fc00::/64" || echo no_tun_routes6'
cap "$V" docker exec "$A" sha256sum /etc/resolv.conf
cap "$V" sh -c 'echo "container cleanup: removing '"$A"' '"$B"' '"$NET"'"'

echo "R16-A spike complete; evidence in $EVID"
