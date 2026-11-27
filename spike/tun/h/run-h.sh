#!/usr/bin/env bash
# R16-H A17 acceptance run (disposable Docker container, isolated netns).
#
# This is acceptance/evidence work, not a product change. It builds the current
# product inside a privileged systemd container, installs the real `tun setup`
# helper with the real systemd units, and drives the CLI/engine/app flows
# against a controlled peer container. Nothing outside the two containers and
# their dedicated bridge network is changed; no public exit is used.
#
# Requirements: Docker, the repository toolchain under .toolchain, local
# rl-tun-spike:base image (or network access to build it), python3.
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
H="$REPO/spike/tun/h"
RUN_ID="${RL_H_RUN_ID:-$(date -u +%Y%m%d%H%M%S)-$$}"
DATE="$(date -u +%Y-%m-%d)"
A="rl-h-${RUN_ID}-a"
B="rl-h-${RUN_ID}-b"
NET="rl-h-${RUN_ID}-net"
WORK="/tmp/opencode/rl-h-${RUN_ID}"
EVID="${RL_H_EVID:-$REPO/spike/tun/evidence/h-tun-${DATE}}"
IMG="rl-tun-spike:base"
KEEP="${RL_H_KEEP:-0}"
# The repository toolchain is normally inside the repo; a clean worktree may
# borrow it from the primary checkout through a nested read-only mount.
TOOLCHAIN="${RL_H_TOOLCHAIN:-$REPO/.toolchain}"

export DOCKER_CONFIG="$WORK/dockerconfig"
mkdir -p "$DOCKER_CONFIG" "$WORK/logs"
# Preserve earlier acceptance evidence: never replace a non-empty evidence
# directory unless the caller explicitly opts in. The initial H run used
# h-tun-<date>; a rerun on a later revision should use a distinct RL_H_EVID.
if [ -d "$EVID" ] && [ -n "$(ls -A "$EVID" 2>/dev/null)" ] && [ "${RL_H_OVERWRITE:-0}" != "1" ]; then
  echo "evidence directory already exists and is not empty: $EVID" >&2
  echo "set RL_H_EVID to a new directory (or RL_H_OVERWRITE=1 to replace it)" >&2
  exit 1
fi
rm -rf "$EVID"
mkdir -p "$EVID"
: >"$EVID/run.log"

log() { printf '\n===== %s =====\n' "$*" | tee -a "$EVID/run.log" >&2; }
say() { printf '%s\n' "$*" | tee -a "$EVID/run.log" >&2; }

cleanup() {
  rc=$?
  set +e
  log "cleanup (exit $rc)"
  # Best-effort graceful stop first; the container removal is the real cleanup.
  docker exec "$A" sh -c 'pkill -TERM -u 1500 -f "run --headless" 2>/dev/null || true' >/dev/null 2>&1
  docker exec "$A" sh -c 'pkill -u 1500 -f "/src/spike/tun" 2>/dev/null || true' >/dev/null 2>&1
  if [ "$KEEP" = "1" ]; then
    say "RL_H_KEEP=1: $A $B $NET kept"
  else
    docker rm -f "$A" "$B" >/dev/null 2>&1
    docker network rm "$NET" >/dev/null 2>&1
  fi
  # Host safety check: the acceptance must not have left a TUN or route here.
  {
    echo "host rl-tun0: $(ip link show rl-tun0 2>&1 || true)"
    echo "host 198.18 route count: $(ip route 2>/dev/null | grep -c '198\.18\.' || true)"
    echo "host fc00::/64 route count: $(ip -6 route 2>/dev/null | grep -c 'fc00::/64' || true)"
    echo "host containers: $(docker ps -a --format '{{.Names}}' 2>/dev/null | grep -c '^rl-h-' || true)"
    echo "host networks: $(docker network ls --format '{{.Name}}' 2>/dev/null | grep -c '^rl-h-' || true)"
    echo "repo status: $(git -C "$REPO" status --porcelain=v1 | wc -l) changed paths"
  } >"$EVID/16-host-cleanup.txt" 2>&1
  sanitize
  exit $rc
}

sanitize() {
  local host
  host="$(hostname 2>/dev/null || true)"
  find "$EVID" -maxdepth 1 -type f -name '*.txt' -print0 2>/dev/null |
    xargs -0 -r sed -i "s#$REPO#<repo>#g; s#$WORK#<work>#g; s#$HOME#<home>#g; s#$RUN_ID#<run>#g"
  if [ -n "$host" ]; then
    find "$EVID" -maxdepth 1 -type f -name '*.txt' -print0 |
      xargs -0 -r sed -i "s#$host#<host>#g"
  fi
  find "$EVID" -maxdepth 1 -type f -name '*.txt' -print0 |
    xargs -0 -r sed -i -E 's/192\.168\.[0-9]+\.[0-9]+/<host-lan-ip>/g; s/([0-9a-fA-F]{2}:){5}[0-9a-fA-F]{2}/<mac>/g; s/\b(wlp|enp|virbr)[a-zA-Z0-9]+\b/<host-iface>/g'
}
trap cleanup EXIT

# ---------------------------------------------------------------- preflight
log "preflight"
docker image inspect "$IMG" >/dev/null 2>&1 || docker build --network host -t "$IMG" "$REPO/spike/tun/docker" >>"$EVID/run.log" 2>&1
if ip link show rl-tun0 >/dev/null 2>&1; then
  echo "rl-tun0 already exists on the host; refusing to run" >&2
  exit 1
fi
if [ -n "$(git -C "$REPO" status --porcelain=v1)" ]; then
  echo "warning: working tree is not clean; the evidence records the current HEAD" >&2
fi
HEAD="$(git -C "$REPO" rev-parse HEAD)"
say "repo=$REPO HEAD=$HEAD date=$DATE run=$RUN_ID"

# ---------------------------------------------------------------- containers
log "creating network and containers"
docker network create --driver bridge --ipv6 \
  --subnet 172.31.77.0/24 --subnet fd00:77::/64 "$NET" >>"$EVID/run.log"
docker run -d --name "$A" --hostname "$A" --privileged --cgroupns=private \
  --tmpfs /run --tmpfs /tmp:rw,exec,size=512m --tmpfs /work:rw,exec,size=1024m \
  --network "$NET" --ip 172.31.77.10 --ip6 fd00:77::10 \
  -v "$REPO:/src:ro" -v "$TOOLCHAIN:/toolchain:ro" "$IMG" >>"$EVID/run.log"
docker run -d --name "$B" --hostname "$B" --cap-add=NET_ADMIN \
  --tmpfs /work:rw,exec,size=256m \
  --network "$NET" --ip 172.31.77.20 --ip6 fd00:77::20 \
  -v "$REPO:/src:ro" -v "$TOOLCHAIN:/toolchain:ro" --entrypoint /bin/bash "$IMG" -c 'sleep infinity' \
  >>"$EVID/run.log"

for _ in $(seq 1 60); do
  s="$(docker exec "$A" systemctl is-system-running 2>/dev/null || true)"
  case "$s" in running|degraded) break;; esac
  sleep 1
done
docker exec "$A" test -S /run/systemd/private
docker exec "$A" test -c /dev/net/tun

# ---------------------------------------------------------------- versions
log "versions and toolchain"
V="$EVID/00-environment.txt"
: >"$V"
{
  echo "date: $(date -u +%FT%TZ)"
  echo "repo HEAD: $HEAD"
  git -C "$REPO" log -1 --oneline
  echo "docker: $(docker version --format '{{.Server.Version}}')"
  docker image inspect "$IMG" --format 'image: {{.Id}}'
  docker image inspect ubuntu:26.04 --format 'base: {{.Id}}'
  echo "host kernel: $(uname -r)"
  docker exec "$A" sh -c 'echo "container kernel: $(uname -r)"; echo "os: $(. /etc/os-release; echo $PRETTY_NAME)"; systemctl --version | head -1; ip -V'
  echo "go (container):"
  docker exec -e GOROOT=/toolchain/go -e PATH=/toolchain/go/bin:/usr/sbin:/usr/bin:/sbin:/bin \
    -e GOTOOLCHAIN=local "$A" go version
} >>"$V" 2>&1

# ---------------------------------------------------------------- build
log "building current product inside the container"
BUILD="$EVID/01-build-gated.txt"
: >"$BUILD"
{
  echo '$ scripts/build.sh (container, ARCHES=amd64)'
  docker exec -e GOROOT=/toolchain/go -e PATH=/toolchain/go/bin:/usr/sbin:/usr/bin:/sbin:/bin \
    -e GOTOOLCHAIN=local -e GOMODCACHE=/toolchain/modcache -e GOCACHE=/work/gocache \
    -e GOFLAGS=-mod=readonly -e HOME=/root \
    "$A" sh -c "cd /src && VERSION=h-accept COMMIT=${HEAD:0:7} DIST=/work/dist ARCHES=amd64 ./scripts/build.sh" 2>&1
  echo
  echo '$ /work/dist/routelatch-linux-amd64 version'
  docker exec "$A" /work/dist/routelatch-linux-amd64 version 2>&1
} >>"$BUILD" 2>&1

# ---------------------------------------------------------------- gated tests
log "gated CLI/service tests (real TUN, isolated netns)"
{
  echo '$ go test -count=1 -run "^(TestTunIntegration|TestTunEngineIntegration)$" -v ./internal/tun'
  docker exec -e GOROOT=/toolchain/go -e PATH=/toolchain/go/bin:/usr/sbin:/usr/bin:/sbin:/bin \
    -e GOTOOLCHAIN=local -e GOMODCACHE=/toolchain/modcache -e GOCACHE=/work/gocache \
    -e GOFLAGS=-mod=readonly -e HOME=/root -e RL_TUN_TEST=1 -e RL_TUN_ISOLATED_NETNS=1 \
    -e CGO_ENABLED=0 \
    "$A" sh -c 'cd /src && go test -count=1 -run "^(TestTunIntegration|TestTunEngineIntegration)$" -v ./internal/tun' 2>&1
} >>"$BUILD" 2>&1

# The gated test must not leave a coverage link behind before the CLI scenario.
if docker exec "$A" ip link show rl-tun0 >/dev/null 2>&1; then
  echo "gated test left rl-tun0 behind" >>"$BUILD"
  docker exec "$A" ip link del rl-tun0 >/dev/null 2>&1 || true
fi

# ---------------------------------------------------------------- peer fixtures
log "configuring peer fixtures"
docker exec "$A" sh -c 'id rl >/dev/null 2>&1 || useradd -u 1500 -m -s /bin/bash rl; install -d -m 700 -o rl -g rl /home/rl/data; install -d -m 755 -o rl -g rl /home/rl/work; grep -q canary2-h.invalid /etc/hosts || echo "203.0.113.8 canary2-h.invalid" >> /etc/hosts'
docker exec "$B" sh -c 'mkdir -p /work/logs'
openssl req -x509 -newkey rsa:2048 -nodes \
  -keyout "$WORK/logs/key.pem" -out "$WORK/logs/cert.pem" -days 2 \
  -subj '/CN=canary-h.invalid' \
  -addext 'subjectAltName=DNS:canary-h.invalid,DNS:canary6-h.invalid,DNS:canary-http-h.invalid,DNS:canary2-h.invalid,IP:203.0.113.7,IP:203.0.113.8,IP:2001:db8::7,IP:2001:db8::8' \
  >>"$EVID/run.log" 2>&1
docker exec -i "$B" sh -c 'cat > /work/logs/cert.pem' <"$WORK/logs/cert.pem"
docker exec -i "$B" sh -c 'cat > /work/logs/key.pem' <"$WORK/logs/key.pem"

# The peer owns the simulated off-link destinations used by the app. 203.0.113.7
# is the approved destination; .8 is deliberately unapproved (targeted direct /
# all-proxy pool / R-28 direct-path probe).
docker exec "$B" sh -c '
  set -e
  ip addr add 203.0.113.7/32 dev eth0 2>/dev/null || true
  ip addr add 203.0.113.8/32 dev eth0 2>/dev/null || true
  ip -6 addr add 2001:db8::7/128 dev eth0 2>/dev/null || true
  ip -6 addr add 2001:db8::8/128 dev eth0 2>/dev/null || true
  python3 /src/spike/tun/fixture/peer_server.py --cert /work/logs/cert.pem --key /work/logs/key.pem \
    --log /work/logs/peer.log --udp-log /work/logs/udp.log >/work/logs/peer.out 2>&1 &
  python3 /src/spike/tun/h/source_server.py --log /work/logs/source.log >/work/logs/source.out 2>&1 &
  python3 /src/spike/tun/h/socks_exit.py --listen 172.31.77.20 --port 11080 --log /work/logs/exit.log \
    --map canary-h.invalid=203.0.113.7 --map canary6-h.invalid=2001:db8::7 \
    --map canary-http-h.invalid=203.0.113.7 --map canary2-h.invalid=203.0.113.8 \
    >/work/logs/exit.out 2>&1 &
  dnsmasq --no-daemon --no-resolv --no-hosts --bind-interfaces \
    --listen-address=203.0.113.7 --listen-address=2001:db8::7 \
    --address=/canary-h.invalid/203.0.113.7 --address=/canary-h.invalid/2001:db8::7 \
    --log-queries --log-facility=/work/logs/dns.log >/work/logs/dnsmasq.out 2>&1 &
  sleep 1
' >>"$EVID/run.log" 2>&1
for _ in $(seq 1 40); do
  if docker exec "$B" sh -c 'ss -lnt | grep -q ":7101 " && ss -lnt | grep -q ":7110 " && ss -lnt | grep -q ":11080 "' 2>/dev/null; then break; fi
  sleep 0.25
done
docker exec "$B" sh -c 'ss -lnt | grep -E ":(7101|7102|7104|7110|11080) "; ss -lun | grep -E ":(53|9999|443) "' >"$EVID/00-peer-listeners.txt" 2>&1 || true

# A's default routes go through the peer so the engine's *direct* dial can reach
# the simulated off-link destination. TUN halves are more specific, so the
# application's traffic is still captured by rl-tun0. This is test-only setup.
log "pointing container default routes at the peer fixture"
docker exec "$A" sh -c 'ip route replace default via 172.31.77.20 dev eth0; ip -6 route replace default via fd00:77::20 dev eth0' \
  >>"$EVID/run.log" 2>&1

# ---------------------------------------------------------------- scenario
log "running A17 scenario"
set +e
python3 "$H/scenario.py" \
  --a "$A" --b "$B" --binary /work/dist/routelatch-linux-amd64 --data /home/rl/data \
  --evidence "$EVID" --repo "$REPO" \
  | tee -a "$EVID/run.log"
SCENARIO_RC=${PIPESTATUS[0]}
set -e
say "scenario exit: $SCENARIO_RC"
exit "$SCENARIO_RC"
