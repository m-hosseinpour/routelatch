#!/bin/bash
set -eu
NET=r16j-internal
TUN=r16j-tun
PEER=r16j-peer
PEERIP=172.31.90.20
OUT="${WORK:?set WORK to the fixture directory}/out/tun"
mkdir -p "$OUT"
cleanup() {
  docker rm -f "$TUN" "$PEER" >/dev/null 2>&1 || true
  docker network rm "$NET" >/dev/null 2>&1 || true
}
trap cleanup EXIT
cleanup
docker network create --internal --subnet 172.31.90.0/24 "$NET" >/dev/null
docker run -d --name "$PEER" --network "$NET" --ip "$PEERIP" -v "${WORK:?set WORK to the fixture directory}:/w" rl-tun-spike:base \
  sh -c 'python3 -u /w/udp_peer.py 9000' >/dev/null
docker run -d --name "$TUN" --privileged --network "$NET" --ip 172.31.90.10 -v "${WORK:?set WORK to the fixture directory}:/w" rl-tun-spike:base \
  sh -c 'sleep infinity' >/dev/null
sleep 1
docker exec -d "$TUN" sh -c '/w/bin/sing-box run -c /w/conf/singbox-relay-tun.json >/w/out/tun/relay2.log 2>&1'
sleep 0.8

run_case() {
  local name="$1" cfg="$2"
  docker exec -d "$TUN" sh -c "/w/bin/xray run -c $cfg >/w/out/tun/xray-$name.log 2>&1"
  for i in $(seq 1 50); do
    docker exec "$TUN" sh -c 'ip -br addr show rl-r16j0' >/dev/null 2>&1 && break
    sleep 0.1
  done
  docker exec "$TUN" ip route add "$PEERIP/32" dev rl-r16j0 2>/dev/null || true
  for i in 1 2 3; do
    docker exec "$TUN" python3 /w/tun_app.py "$PEERIP" 9000 "$name-$i"
  done
  docker exec "$TUN" pkill -f "xray run -c $cfg" || true
  sleep 0.8
  docker exec "$TUN" ip route del "$PEERIP/32" dev rl-r16j0 2>/dev/null || true
  sleep 0.3
}
echo "== sniff enabled (shipped config)"; run_case sniff-on /w/conf/xray-tun-udp.json
echo "== sniff disabled"; run_case sniff-off /w/conf/xray-tun-udp-nosniff.json
