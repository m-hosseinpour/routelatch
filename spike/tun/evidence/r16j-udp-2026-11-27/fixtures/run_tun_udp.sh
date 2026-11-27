#!/bin/bash
set -eu
NET=r16j-internal
TUN=r16j-tun
PEER=r16j-peer
TUNIP=172.31.90.10
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
docker run -d --name "$TUN" --privileged --network "$NET" --ip "$TUNIP" -v "${WORK:?set WORK to the fixture directory}:/w" rl-tun-spike:base \
  sh -c 'sleep infinity' >/dev/null
sleep 1

echo "== peer log (start)"; docker logs "$PEER" 2>&1 | tail -2
echo "== start relay"
docker exec -d "$TUN" sh -c '/w/bin/sing-box run -c /w/conf/singbox-relay-tun.json >/w/out/tun/relay.log 2>&1'
sleep 0.8
echo "== start xray tun"
docker exec -d "$TUN" sh -c '/w/bin/xray run -c /w/conf/xray-tun-udp.json >/w/out/tun/xray.log 2>&1'
for i in $(seq 1 50); do
  if docker exec "$TUN" sh -c 'ip -br addr show rl-r16j0' >/dev/null 2>&1; then break; fi
  sleep 0.1
done
echo "== link/routes"
docker exec "$TUN" sh -c 'ip -br addr show rl-r16j0; ip route | grep rl-r16j0; ip -6 route | grep rl-r16j0'
echo "== force app destination through TUN"
docker exec "$TUN" ip route add "$PEERIP/32" dev rl-r16j0
docker exec "$TUN" ip route get "$PEERIP"
echo "== app UDP through TUN (live relay)"
docker exec "$TUN" python3 /w/tun_app.py "$PEERIP" 9000 tun-live-1
docker exec "$TUN" python3 /w/tun_app.py "$PEERIP" 9000 tun-live-2
echo "== peer count"; docker logs "$PEER" 2>&1 | grep -c datagram || true
echo "== kill relay (simulated dead pool)"
docker exec "$TUN" pkill -f 'sing-box run -c /w/conf/singbox-relay-tun.json' || true
sleep 0.5
echo "== app UDP through TUN (dead relay)"
docker exec "$TUN" python3 /w/tun_app.py "$PEERIP" 9000 tun-dead-1
echo "== peer count after dead relay (must stay 2)"; docker logs "$PEER" 2>&1 | grep -c datagram || true
echo "== stop xray and check cleanup"
docker exec "$TUN" pkill -f 'xray run -c /w/conf/xray-tun-udp.json' || true
sleep 1
docker exec "$TUN" sh -c 'ip -br addr show rl-r16j0 2>&1 || true; ip route | grep rl-r16j0 || true; ip -6 route | grep rl-r16j0 || true'
echo "== relay log"; docker exec "$TUN" sh -c 'tail -12 /w/out/tun/relay.log' || true
