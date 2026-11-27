#!/usr/bin/env bash
# R16-H addendum: IPv6 DNS/53, fail-open status visibility, package checks.
#
# Same disposable privileged container pattern as run-h.sh: product TUN state
# exists only inside container A, the peer fixtures are in container B, and the
# package checks use an isolated DIST inside A. Nothing on the host is changed.
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
H="$REPO/spike/tun/h"
RUN_ID="${RL_H_ADDENDUM_RUN_ID:-$(date -u +%Y%m%d%H%M%S)-$$}"
DATE="$(date -u +%Y-%m-%d)"
A="rl-ha-${RUN_ID}-a"
B="rl-ha-${RUN_ID}-b"
NET="rl-ha-${RUN_ID}-net"
WORK="/tmp/opencode/rl-ha-${RUN_ID}"
EVID="${RL_H_ADDENDUM_EVID:-$REPO/spike/tun/evidence/h-tun-${DATE}-addendum}"
IMG="rl-tun-spike:base"
KEEP="${RL_H_KEEP:-0}"
TOOLCHAIN="${RL_H_TOOLCHAIN:-$REPO/.toolchain}"

export DOCKER_CONFIG="$WORK/dockerconfig"
mkdir -p "$DOCKER_CONFIG" "$WORK/logs"
if [ -d "$EVID" ] && [ -n "$(ls -A "$EVID" 2>/dev/null)" ] && [ "${RL_H_OVERWRITE:-0}" != "1" ]; then
  echo "evidence directory already exists and is not empty: $EVID" >&2
  echo "set RL_H_ADDENDUM_EVID to a new directory (or RL_H_OVERWRITE=1 to replace it)" >&2
  exit 1
fi
rm -rf "$EVID"
mkdir -p "$EVID"
: >"$EVID/run.log"
printf '# R16-H addendum results\n' >"$EVID/results.txt"

log() { printf '\n===== %s =====\n' "$*" | tee -a "$EVID/run.log" >&2; }
say() { printf '%s\n' "$*" | tee -a "$EVID/run.log" >&2; }

cleanup() {
  rc=$?
  set +e
  log "cleanup (exit $rc)"
  docker exec "$A" sh -c 'pkill -TERM -u 1500 -f "run --headless" 2>/dev/null || true' >/dev/null 2>&1
  if [ "$KEEP" = "1" ]; then
    say "RL_H_KEEP=1: $A $B $NET kept"
  else
    docker rm -f "$A" "$B" >/dev/null 2>&1
    docker network rm "$NET" >/dev/null 2>&1
  fi
  {
    echo "host rl-tun0: $(ip link show rl-tun0 2>&1 || true)"
    echo "host 198.18 route count: $(ip route 2>/dev/null | grep -c '198\.18\.' || true)"
    echo "host fc00::/64 route count: $(ip -6 route 2>/dev/null | grep -c 'fc00::/64' || true)"
    echo "host containers: $(docker ps -a --format '{{.Names}}' 2>/dev/null | grep -c '^rl-ha-' || true)"
    echo "host networks: $(docker network ls --format '{{.Name}}' 2>/dev/null | grep -c '^rl-ha-' || true)"
    echo "repo status: $(git -C "$REPO" status --porcelain=v1 | wc -l) changed paths"
  } >"$EVID/90-final-cleanup.txt" 2>&1
  sanitize
  exit $rc
}

sanitize() {
  local host
  host="$(hostname 2>/dev/null || true)"
  find "$EVID" -maxdepth 1 -type f -print0 2>/dev/null |
    xargs -0 -r sed -i "s#$REPO#<repo>#g; s#$WORK#<tmp>#g; s#/tmp/opencode#<tmp>#g; s#$HOME#<home>#g; s#$RUN_ID#<run>#g"
  if [ -n "$host" ]; then
    find "$EVID" -maxdepth 1 -type f -print0 |
      xargs -0 -r sed -i "s#$host#<host>#g"
  fi
  find "$EVID" -maxdepth 1 -type f -print0 |
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
  echo "warning: working tree is not untracked-only; the evidence records the current HEAD" >&2
fi
HEAD="$(git -C "$REPO" rev-parse HEAD)"
say "repo=$REPO HEAD=$HEAD date=$DATE run=$RUN_ID"

# ---------------------------------------------------------------- containers
log "creating network and containers"
docker network create --driver bridge --ipv6 \
  --subnet 172.31.78.0/24 --subnet fd00:78::/64 "$NET" >>"$EVID/run.log"
docker run -d --name "$A" --hostname "$A" --privileged --cgroupns=private \
  --tmpfs /run --tmpfs /tmp:rw,exec,size=512m --tmpfs /work:rw,exec,size=3072m \
  --network "$NET" --ip 172.31.78.10 --ip6 fd00:78::10 \
  -v "$REPO:/src:ro" -v "$TOOLCHAIN:/toolchain:ro" \
  -v /usr/bin/file:/usr/bin/file:ro \
  -v /usr/lib/x86_64-linux-gnu/libmagic.so.1:/usr/lib/x86_64-linux-gnu/libmagic.so.1:ro \
  -v /usr/lib/file/magic.mgc:/usr/lib/file/magic.mgc:ro \
  "$IMG" >>"$EVID/run.log"
docker run -d --name "$B" --hostname "$B" --cap-add=NET_ADMIN \
  --tmpfs /work:rw,exec,size=256m \
  --network "$NET" --ip 172.31.78.20 --ip6 fd00:78::20 \
  -v "$REPO:/src:ro" --entrypoint /bin/bash "$IMG" -c 'sleep infinity' \
  >>"$EVID/run.log"

for _ in $(seq 1 60); do
  s="$(docker exec "$A" systemctl is-system-running 2>/dev/null || true)"
  case "$s" in running|degraded) break;; esac
  sleep 1
done
docker exec "$A" test -S /run/systemd/private
docker exec "$A" test -c /dev/net/tun

V="$EVID/00-environment.txt"
{
  echo "date: $(date -u +%FT%TZ)"
  echo "repo HEAD: $HEAD"
  git -C "$REPO" log -1 --oneline
  echo "docker: $(docker version --format '{{.Server.Version}}')"
  docker image inspect "$IMG" --format 'image: {{.Id}}'
  echo "host kernel: $(uname -r)"
  docker exec "$A" sh -c 'echo "container kernel: $(uname -r)"; echo "os: $(. /etc/os-release; echo $PRETTY_NAME)"; systemctl --version | head -1; ip -V'
  echo "go (container):"
  docker exec -e GOROOT=/toolchain/go -e PATH=/toolchain/go/bin:/usr/sbin:/usr/bin:/sbin:/bin \
    -e GOTOOLCHAIN=local "$A" go version
  echo "file (mounted for check-artifact):"
  docker exec -e MAGIC=/usr/lib/file/magic.mgc "$A" file --version
} >"$V" 2>&1

DENV=(-e GOROOT=/toolchain/go -e PATH=/toolchain/go/bin:/usr/sbin:/usr/bin:/sbin:/bin
      -e GOTOOLCHAIN=local -e GOMODCACHE=/toolchain/modcache -e GOCACHE=/work/gocache
      -e GOFLAGS=-mod=readonly -e HOME=/root -e MAGIC=/usr/lib/file/magic.mgc)

# ---------------------------------------------------------------- package checks
log "package/artifact checks at $HEAD in an isolated DIST"
PKG="$EVID/03-package.txt"
: >"$PKG"
run_pkg() {
  echo "\$ $*" >>"$PKG"
  docker exec "${DENV[@]}" "$A" sh -c "cd /src && $*" >>"$PKG" 2>&1
  rc=$?
  echo "[exit $rc]" >>"$PKG"
  echo >>"$PKG"
  return $rc
}
set +e
run_pkg "VERSION=h-accept COMMIT=${HEAD:0:7} DIST=/work/pkgdist ARCHES='amd64 arm64 riscv64 ppc64le' REQUIRE_DEB=1 scripts/build.sh" \
  && run_pkg "VERSION=h-accept COMMIT=${HEAD:0:7} DIST=/work/pkgdist REQUIRE_DEB=1 scripts/package.sh" \
  && run_pkg "DIST=/work/pkgdist ARCHES='amd64 arm64 riscv64 ppc64le' scripts/check-package.sh" \
  && run_pkg "DIST=/work/pkgdist ROUTELATCH_CHECK_HTTP_PORT=19317 ROUTELATCH_CHECK_SOCKS_PORT=19318 scripts/check-artifact.sh /work/pkgdist/routelatch-linux-amd64"
PKG_RC=$?
set -e
say "package checks exit: $PKG_RC"

# Explicit no-unit/tmpfiles/helper/maintainer-script witness, mirroring CI.
{
  echo '$ explicit package payload assertions'
  docker exec "${DENV[@]}" "$A" sh -c '
    set -eu
    fail=0
    for artifact in /work/pkgdist/packages/*.tar.gz; do
      if tar -tzf "$artifact" | grep -Eq "(\.service$|\.desktop$|(^|/)systemd(/|$)|(^|/)autostart/|routelatch-tun|tmpfiles|tun-service)"; then
        echo "bad tar payload: $artifact"; fail=1
      fi
    done
    for artifact in /work/pkgdist/packages/*.deb; do
      if dpkg-deb --fsys-tarfile "$artifact" | tar -tf - |
        grep -Eq "(\.service$|\.desktop$|/usr/lib/systemd/|/usr/share/routelatch/systemd/|routelatch-tun|tmpfiles|tun-service)"; then
        echo "bad deb payload: $artifact"; fail=1
      fi
      members=$(dpkg-deb --info "$artifact" | sed -n "s/^ *//p" | grep -E "postinst|postrm|preinst|prerm" || true)
      if [ -n "$members" ]; then echo "bad deb control member: $artifact $members"; fail=1; fi
    done
    if grep -Eiq "systemd|routelatch\.service|\.desktop|postinst|postrm|preinst|prerm|tmpfiles|tun-service" /work/pkgdist/packages/*.spec; then
      echo "bad RPM spec"; fail=1
    fi
    if [ "$fail" -ne 0 ]; then exit 1; fi
    echo "packages ship no unit, tmpfiles, helper or maintainer script"
  '
  echo "[exit $?]"
} >>"$PKG" 2>&1
if grep -q "packages ship no unit, tmpfiles, helper or maintainer script" "$PKG"; then
  echo "PASS package no-unit/tmpfiles/helper/postinst" >>"$EVID/results.txt"
else
  echo "FAIL package no-unit/tmpfiles/helper/postinst" >>"$EVID/results.txt"
fi
if [ "$PKG_RC" -eq 0 ]; then
  echo "PASS package build/check-package/check-artifact" >>"$EVID/results.txt"
else
  echo "FAIL package build/check-package/check-artifact :: exit=$PKG_RC" >>"$EVID/results.txt"
fi

# ---------------------------------------------------------------- peer fixtures
log "configuring IPv6 DNS peer"
docker exec "$B" sh -c '
  set -e
  mkdir -p /work/logs
  ip addr add 203.0.113.7/32 dev eth0 2>/dev/null || true
  ip -6 addr add 2001:db8::7/128 dev eth0 nodad 2>/dev/null || true
  python3 /src/spike/tun/h/v6_dns_server.py --listen 2001:db8::7 --log /work/logs/v6dns.log >/work/logs/v6dns.out 2>&1 &
  sleep 1
' >>"$EVID/run.log" 2>&1
for _ in $(seq 1 40); do
  if docker exec "$B" sh -c 'test -f /work/logs/v6dns.log && grep -q "\"event\": \"listen\"" /work/logs/v6dns.log' 2>/dev/null; then break; fi
  sleep 0.25
done
{
  docker exec "$B" sh -c 'ip -6 addr show dev eth0; echo ---; cat /work/logs/v6dns.log 2>&1; echo ---; cat /work/logs/v6dns.out 2>&1; echo ---; ss -lun; ss -ltn'
} >"$EVID/00-peer-listeners.txt" 2>&1 || true

log "pointing container routes at the peer fixture"
docker exec "$A" sh -c 'ip route replace default via 172.31.78.20 dev eth0; ip -6 route replace default via fd00:78::20 dev eth0' \
  >>"$EVID/run.log" 2>&1

# ---------------------------------------------------------------- build and engine
log "building product and installing the helper"
BUILD="$EVID/01-build.txt"
: >"$BUILD"
{
  echo "\$ scripts/build.sh (container, ARCHES=amd64)"
  docker exec "${DENV[@]}" "$A" sh -c "cd /src && VERSION=h-accept COMMIT=${HEAD:0:7} DIST=/work/dist ARCHES=amd64 ./scripts/build.sh" 2>&1
  echo
  echo '$ /work/dist/routelatch-linux-amd64 version'
  docker exec "$A" /work/dist/routelatch-linux-amd64 version 2>&1
} >>"$BUILD" 2>&1

docker exec "$A" sh -c 'id rl >/dev/null 2>&1 || useradd -u 1500 -m -s /bin/bash rl; install -d -m 700 -o rl -g rl /home/rl/data'
docker exec "$A" sh -c "SUDO_UID=1500 SUDO_GID=1500 /work/dist/routelatch-linux-amd64 --json tun setup --socks 127.0.0.1:8318" \
  >"$EVID/02-setup.txt" 2>&1
docker exec -d -u 1500:1500 -e HOME=/home/rl "$A" sh -c \
  'exec /work/dist/routelatch-linux-amd64 --data-dir /home/rl/data run --headless >>/home/rl/engine.out 2>&1'
for _ in $(seq 1 60); do
  r="$(docker exec -u 1500:1500 -e HOME=/home/rl "$A" /work/dist/routelatch-linux-amd64 --json --data-dir /home/rl/data status 2>/dev/null || true)"
  if printf '%s' "$r" | grep -q '"ready":true'; then break; fi
  sleep 0.5
done
docker exec -u 1500:1500 -e HOME=/home/rl "$A" /work/dist/routelatch-linux-amd64 --json --data-dir /home/rl/data status \
  >>"$EVID/02-setup.txt" 2>&1

# ---------------------------------------------------------------- addendum checks
log "running IPv6 DNS and fail-open status checks"
set +e
python3 "$H/addendum.py" --a "$A" --b "$B" --binary /work/dist/routelatch-linux-amd64 \
  --data /home/rl/data --evidence "$EVID" | tee -a "$EVID/run.log"
ADDENDUM_RC=${PIPESTATUS[0]}
set -e
say "addendum exit: $ADDENDUM_RC"

# ---------------------------------------------------------------- final cleanup
log "final no-orphan checks"
set +e
docker exec -u 1500:1500 -e HOME=/home/rl "$A" /work/dist/routelatch-linux-amd64 --json --data-dir /home/rl/data tun off >>"$EVID/run.log" 2>&1
docker exec "$A" pkill -TERM -u 1500 -f 'run --headless' >/dev/null 2>&1
sleep 1
docker exec "$A" sh -c "SUDO_UID=1500 SUDO_GID=1500 /work/dist/routelatch-linux-amd64 --json tun uninstall" >>"$EVID/run.log" 2>&1
set -e
{
  docker exec "$A" sh -c 'ip link show rl-tun0 2>&1 || true; echo ---; ip route | grep -E "198\\.18\\.|^0\\.0\\.0\\.0/1|^128\\.0\\.0\\.0/1" || true; echo ---; ip -6 route | grep -E "fc00::/64|^::/1|^8000::/1" || true; echo ---; pgrep -u 1500 -a -f "run --headless" || true; echo ---; ls -la /run/routelatch-tun 2>&1 || true'
} >"$EVID/91-container-cleanup.txt" 2>&1

passed=$(grep -c '^PASS ' "$EVID/results.txt" || true)
failed=$(grep -c '^FAIL ' "$EVID/results.txt" || true)
notrun=$(grep -c '^NOTRUN ' "$EVID/results.txt" || true)
echo "SUMMARY-ALL passed=$passed failed=$failed notrun=$notrun" >>"$EVID/results.txt"

FINAL_RC="$ADDENDUM_RC"
if [ "$PKG_RC" -ne 0 ] || [ "$failed" -ne 0 ]; then
  FINAL_RC=1
fi
exit "$FINAL_RC"
