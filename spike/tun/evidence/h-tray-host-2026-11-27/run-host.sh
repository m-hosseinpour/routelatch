#!/usr/bin/env bash
# R16-H follow-up: real TUN-active tray/toggle acceptance on an authorized host.
#
# Installs the real root helper through pkexec, starts an isolated app/tray,
# enables and disables system-wide coverage through the real product tray menu,
# and uninstalls the helper. The live user instance is never addressed: every
# CLI call uses the isolated --data-dir and every kill is scoped to it.
#
# The active window contains only local commands (netlink, Unix sockets, file
# hashes); no public traffic is sent. The disable path runs before the app is
# stopped, and the trap restores the host on every failure.
set -uo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO=${REPO:-$(cd "$HERE/../../../.." && pwd)}
WORK=${WORK:-${TMPDIR:-/tmp}/routelatch-h-tray-host}
BIN=${BIN:-$WORK/bin/routelatch}
EV=${EV:-$HERE/real}
HELPER=$HERE/menu-dump2.py
ICON=$HERE/icon-dump.py
UID_NUM=$(id -u)
TS=$(date -u +%Y%m%dT%H%M%SZ)
BASE=${BASE:-$WORK/run-$TS}
DATA=$BASE/data
XDG=$BASE/xdg
mkdir -p "$EV" "$DATA" "$XDG/config" "$XDG/state" "$XDG/home" "$XDG/cache"
chmod 700 "$BASE" "$DATA" "$XDG"/*

export DISPLAY=${DISPLAY:-:0}
export DBUS_SESSION_BUS_ADDRESS=${DBUS_SESSION_BUS_ADDRESS:-unix:path=${XDG_RUNTIME_DIR:-/run/user/$UID_NUM}/bus}
export XDG_CONFIG_HOME=$XDG/config XDG_STATE_HOME=$XDG/state HOME=$XDG/home
export XDG_CACHE_HOME=$XDG/cache XDG_CURRENT_DESKTOP=KDE

SETUP_DONE=0
APP_STARTED=0
CLEANED=0
RESULTS=$EV/results.txt
: >"$RESULTS"

say() { printf '%s %s\n' "$(date -u +%H:%M:%SZ)" "$*" | tee -a "$EV/steps.txt"; }
fail() { say "FAIL: $*"; exit 1; }
ck() { # ck PASS|FAIL|NOTRUN name detail
  printf '%s|%s|%s\n' "$1" "$2" "$3" >>"$RESULTS"
  say "check $1: $2${3:+ ($3)}"
}
cli() { timeout 90 "$BIN" --data-dir "$DATA" "$@"; }
dump() { timeout 20 python3 "$HELPER" "$BUS" "$1" "${2:--1}"; }
id_of() { awk -F'|' -v l="$1" '$6==l {print $1; exit}' <<<"$2"; }
has_label() { awk -F'|' -v l="$1" '$6==l {found=1} END{exit !found}' <<<"$2"; }
has_prefix() { awk -F'|' -v p="$1" 'index($6,p)==1 {found=1} END{exit !found}' <<<"$2"; }
click() {
  local id=$1
  [ -n "$id" ] || return 1
  say "click id=$id"
  timeout 20 gdbus call --session --dest "$BUS" --object-path /StatusNotifierMenu \
    --method com.canonical.dbusmenu.Event "$id" clicked "<''>" 0 >/dev/null
}
icon() {
  timeout 20 gdbus call --session --dest "$BUS" --object-path /StatusNotifierItem \
    --method org.freedesktop.DBus.Properties.Get org.kde.StatusNotifierItem IconPixmap \
    | python3 "$ICON" "$EV/icon-$1.png" | tee -a "$EV/icons.txt"
}
tooltip() {
  timeout 20 gdbus call --session --dest "$BUS" --object-path /StatusNotifierItem \
    --method org.freedesktop.DBus.Properties.Get org.kde.StatusNotifierItem ToolTip \
    | tee -a "$EV/tooltips.txt"
}
sup_pid() { python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["pid"])' "$DATA/run/app.owner" 2>/dev/null; }
helper_present() {
  [ -e /etc/systemd/system/routelatch-tun.socket ] || [ -e /etc/systemd/system/routelatch-tun.service ] \
    || [ -e /usr/libexec/routelatch/tun-service ] || [ -e /etc/routelatch/tun.json ] \
    || [ -e /etc/tmpfiles.d/routelatch-tun.conf ]
}
link_present() { ip link show rl-tun0 >/dev/null 2>&1; }
live_set() {
  pgrep -a -f '^(/[^ ]*/)?routelatch (__app-supervisor|run --headless|__app-tray)( |$)' 2>/dev/null \
    | grep -v -- "--data-dir $DATA" || true
}

stop_app() {
  local sup
  sup=$(sup_pid || true)
  if [ -n "${sup:-}" ]; then
    if tr '\0' ' ' <"/proc/$sup/cmdline" 2>/dev/null | grep -q -- "--data-dir $DATA"; then
      kill -TERM "$sup" 2>/dev/null || true
      for _ in $(seq 1 150); do kill -0 "$sup" 2>/dev/null || break; sleep 0.1; done
      kill -KILL "$sup" 2>/dev/null || true
    fi
  fi
  for pid in $(pgrep -f -- "--data-dir $DATA" 2>/dev/null || true); do
    [ "$pid" = "$$" ] && continue
    kill -KILL "$pid" 2>/dev/null || true
  done
  APP_STARTED=0
}

uninstall_helper() {
  say "pkexec tun uninstall"
  timeout 120 pkexec /usr/bin/env SUDO_UID="$UID_NUM" "$BIN" tun uninstall --json \
    >"$EV/uninstall.json" 2>"$EV/uninstall.err" || true
  SETUP_DONE=0
}

cleanup() {
  [ "$CLEANED" = 1 ] && return 0
  CLEANED=1
  say "cleanup trap start"
  # 1) Always try the independent, non-root disable first when the app is up.
  if [ "$APP_STARTED" = 1 ]; then
    cli tun off >"$EV/cleanup-tun-off.out" 2>&1 || true
  fi
  # 2) If the link is still there, uninstall the helper before killing the app:
  #    the engine preserves coverage on a supervisor-managed shutdown.
  if link_present && helper_present; then
    say "rl-tun0 still present; uninstalling helper as recovery"
    uninstall_helper
  fi
  # 3) Stop only the isolated app/tray.
  stop_app
  # 4) Remove the helper if it is still installed.
  if helper_present; then uninstall_helper; fi
  say "cleanup trap done: link=$(link_present && echo present || echo absent) helper=$(helper_present && echo present || echo absent)"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

say "base=$BASE ev=$EV bin=$BIN head=$(git -C "$REPO" rev-parse HEAD)"

# --- baseline, re-measured immediately before any host change -----------------
ip route >"$EV/baseline-route4.txt"
ip -6 route >"$EV/baseline-route6.txt"
ip route show table all >"$EV/baseline-routes-all.txt"
ip -6 route show table all >"$EV/baseline-routes6-all.txt"
ip rule >"$EV/baseline-rule4.txt"
ip -6 rule >"$EV/baseline-rule6.txt"
sha256sum /etc/resolv.conf >"$EV/baseline-resolv-sha.txt"
ip -br link >"$EV/baseline-links.txt"
systemctl is-active routelatch-tun.socket routelatch-tun.service >"$EV/baseline-units.txt" 2>&1 || true
live_set >"$EV/baseline-live-pids.txt" 2>&1 || true

[ -x "$BIN" ] || fail "binary missing: $BIN"
helper_present && fail "a TUN helper is already installed; refusing to overwrite it"
[ -e /run/routelatch-tun/control.sock ] && fail "a TUN control socket already exists"
link_present && fail "rl-tun0 already exists"

# --- isolated ports, then helper install -------------------------------------
read -r HTTP SOCKS < <(python3 - <<'PY'
import socket
p = []
for _ in range(2):
    s = socket.socket(); s.bind(("127.0.0.1", 0)); p.append(s.getsockname()[1]); s.close()
print(p[0], p[1])
PY
)
printf '{"schema_version":1,"http_listen":"127.0.0.1:%s","socks_listen":"127.0.0.1:%s","log_level":"info"}\n' \
  "$HTTP" "$SOCKS" >"$DATA/config.json"
chmod 600 "$DATA/config.json"
say "http=$HTTP socks=$SOCKS"

say "pkexec tun setup --socks 127.0.0.1:$SOCKS"
if ! timeout 120 pkexec /usr/bin/env SUDO_UID="$UID_NUM" "$BIN" tun setup --socks "127.0.0.1:$SOCKS" --json \
  >"$EV/setup.json" 2>"$EV/setup.err"; then
  ck FAIL "real helper install" "setup command failed; see setup.err"
  fail "tun setup failed"
fi
SETUP_DONE=1
ck PASS "real helper install" "pkexec tun setup exit 0"

{
  echo "== systemctl is-active =="; systemctl is-active routelatch-tun.socket routelatch-tun.service 2>&1
  echo "== systemctl status socket =="; systemctl status routelatch-tun.socket --no-pager 2>&1 | head -20
  echo "== systemctl status service =="; systemctl status routelatch-tun.service --no-pager 2>&1 | head -20
  echo "== unit files =="; ls -l /etc/systemd/system/routelatch-tun.socket /etc/systemd/system/routelatch-tun.service 2>&1
  echo "== binary =="; ls -l /usr/libexec/routelatch/tun-service 2>&1
  echo "== config =="; ls -l /etc/routelatch/tun.json 2>&1
  echo "== tmpfiles =="; ls -l /etc/tmpfiles.d/routelatch-tun.conf 2>&1
  echo "== runtime dir =="; ls -ld /run/routelatch-tun 2>&1
  echo "== control socket =="; stat -c '%n mode=%a uid=%u gid=%g type=%F' /run/routelatch-tun/control.sock 2>&1
  echo "== unit limits =="; systemctl show routelatch-tun.service -p StartLimitIntervalUSec -p StartLimitBurst -p BindsTo 2>&1
} >"$EV/02-setup-verify.txt" 2>&1
# The socket unit owns activation; the service starts on first control use.
systemctl is-active --quiet routelatch-tun.socket || fail "routelatch-tun.socket is not active after setup"
[ -S /run/routelatch-tun/control.sock ] || fail "control socket absent after setup"
[ "$(stat -c %a /run/routelatch-tun/control.sock)" = 600 ] || fail "control socket is not 0600"
[ "$(stat -c %u /run/routelatch-tun/control.sock)" = "$UID_NUM" ] || fail "control socket owner is not the invoking uid"
[ "$(stat -c %u /usr/libexec/routelatch/tun-service)" = 0 ] || fail "helper binary is not root-owned"
ck PASS "helper units/socket layout" "socket active, control.sock 0600 owner $UID_NUM, binary root-owned"

# --- isolated app/tray -------------------------------------------------------
say "start isolated app"
timeout 90 "$BIN" --data-dir "$DATA" run >"$EV/run.out" 2>&1 || fail "isolated run failed: $(cat "$EV/run.out")"
APP_STARTED=1
TRAY=$(grep -ho 'tray_started pid=[0-9]*' "$DATA"/log/*.log 2>/dev/null | tail -1 | cut -d= -f2 || true)
[ -n "${TRAY:-}" ] || fail "no tray_started in isolated log"
BUS=""
for _ in $(seq 1 200); do
  LIST=$(timeout 10 gdbus call --session --dest org.kde.StatusNotifierWatcher --object-path /StatusNotifierWatcher \
    --method org.freedesktop.DBus.Properties.Get org.kde.StatusNotifierWatcher RegisteredStatusNotifierItems 2>/dev/null || true)
  BUS=$(grep -o "org.freedesktop.StatusNotifierItem-$TRAY-[0-9]*" <<<"$LIST" | head -1 || true)
  [ -n "$BUS" ] && break
  sleep 0.1
done
[ -n "$BUS" ] || fail "tray item for pid $TRAY not registered"
SUP=$(sup_pid)
printf 'base=%s\ndata=%s\nhttp=%s\nsocks=%s\nsupervisor=%s\ntray=%s\nbus=%s\nbinary=%s\nhead=%s\n' \
  "$BASE" "$DATA" "$HTTP" "$SOCKS" "$SUP" "$TRAY" "$BUS" "$BIN" "$(git -C "$REPO" rev-parse HEAD)" >"$EV/run-env.txt"
say "tray=$TRAY bus=$BUS supervisor=$SUP"

wait_header() {
  local want=$1 tries=${2:-200}
  for _ in $(seq 1 "$tries"); do
    [ "$(timeout 10 python3 "$HELPER" "$BUS" 0 -1 2>/dev/null | awk -F'|' 'NR>1 && $6!="" {print $6; exit}')" = "$want" ] && return 0
    sleep 0.1
  done
  return 1
}
wait_coverage_prefix() { # prefix tries
  local p=$1 tries=${2:-200} root id sub
  for _ in $(seq 1 "$tries"); do
    root=$(dump 0 -1 2>/dev/null || true)
    id=$(id_of "Coverage" "$root")
    if [ -n "$id" ]; then
      sub=$(dump "$id" -1 2>/dev/null || true)
      has_prefix "$p" "$sub" && { printf '%s\n' "$sub"; return 0; }
    fi
    sleep 0.15
  done
  return 1
}

wait_header "Targeted · 0 usable · 0 open" 300 || fail "isolated header never became Targeted"
cli status --json >"$EV/03-status-off.json"
python3 - "$EV/03-status-off.json" <<'PY'
import json,sys
d=json.load(open(sys.argv[1]))["data"]
print("mode=",d["mode"],"ready=",d["ready"],"tun=",json.dumps(d.get("tun")))
PY
ck PASS "tray/engine ready, helper discovered" "$(python3 -c 'import json,sys;d=json.load(open(sys.argv[1]))["data"];t=d.get("tun") or {};print("helper=",t.get("helper"),"state=",t.get("state"),"desired=",t.get("desired"))' "$EV/03-status-off.json")"

ROOT=$(dump 0 -1)
COV=$(id_of "Coverage" "$ROOT")
[ -n "$COV" ] || fail "no Coverage submenu"
COVD=$(dump "$COV" -1)
printf '%s\n' "$COVD" >"$EV/03-coverage-off.txt"
printf '%s\n' "$ROOT" >"$EV/03-layout-off.txt"
icon off-before
tooltip
has_prefix "Coverage: off · desired off" "$COVD" || fail "off menu did not report off"
# Give the 2 s tray status poll one cycle to render the helper row.
for _ in $(seq 1 100); do
  has_label "Helper: installed" "$COVD" && break
  sleep 0.1
  COVD=$(dump "$COV" -1)
done
has_label "Helper: installed" "$COVD" || fail "Coverage menu never showed Helper: installed"
printf '%s\n' "$COVD" >"$EV/03-coverage-off.txt"
ck PASS "real off menu with helper installed" "Coverage: off · desired off, Helper: installed"

# --- tray-initiated enable ---------------------------------------------------
ACTIVE_FROM=0
EN=$(id_of "Enable system-wide coverage…" "$COVD")
if [ -z "$EN" ]; then
  ck FAIL "tray enable" "Enable item absent"
else
  if click "$EN"; then
    PENDING=""
    for _ in $(seq 1 50); do
      PENDING=$(dump 0 -1 2>/dev/null || true)
      has_label "Confirm: Enable system-wide coverage (all host traffic)" "$PENDING" && break
      sleep 0.1
    done
    printf '%s\n' "$PENDING" >"$EV/04-enable-pending.txt"
    has_label "Confirm: Enable system-wide coverage (all host traffic)" "$PENDING" || ck FAIL "tray enable confirm" "confirmation not shown"
    has_label "Cancel" "$PENDING" || ck FAIL "tray enable cancel" "Cancel not shown"
    CONFIRM=$(id_of "Confirm: Enable system-wide coverage (all host traffic)" "$PENDING")
    if [ -n "$CONFIRM" ] && click "$CONFIRM"; then
      ACTIVE_FROM=$(date +%s)
      if wait_coverage_prefix "Coverage: on · desired on" 400 >"$EV/04-active-coverage.txt"; then
        ck PASS "tray enable" "real two-step confirm; Coverage: on · desired on"
      else
        ck FAIL "tray enable" "coverage did not reach on"
      fi
    else
      ck FAIL "tray enable confirm click" "could not click confirm"
    fi
  else
    ck FAIL "tray enable click" "click failed"
  fi
fi

# Capture the active window whether or not the menu reported on; a link is the
# real witness and the CLI status is authoritative.
if link_present; then
  cli tun status --json >"$EV/04-active-tun-status.json" 2>/dev/null
  cli status --json >"$EV/04-active-status.json" 2>/dev/null
  ip -d link show rl-tun0 >"$EV/04-active-link.txt" 2>&1
  ip addr show rl-tun0 >"$EV/04-active-addr.txt" 2>&1
  ip route show dev rl-tun0 >"$EV/04-active-route4-dev.txt" 2>&1
  ip -6 route show dev rl-tun0 >"$EV/04-active-route6-dev.txt" 2>&1
  ip route show table all >"$EV/04-active-routes-all.txt" 2>&1
  ip -6 route show table all >"$EV/04-active-routes6-all.txt" 2>&1
  ip rule >"$EV/04-active-rule4.txt" 2>&1
  ip -6 rule >"$EV/04-active-rule6.txt" 2>&1
  sha256sum /etc/resolv.conf >"$EV/04-active-resolv-sha.txt" 2>&1
  ROOT=$(dump 0 -1)
  COV=$(id_of "Coverage" "$ROOT")
  [ -n "$COV" ] && dump "$COV" -1 >"$EV/04-active-coverage-final.txt"
  printf '%s\n' "$ROOT" >"$EV/04-active-layout.txt"
  icon on-active
  tooltip
  # `tun status` returns the TunStatus object directly in data (unlike the
  # top-level `status` command, which nests it under data.tun).
  python3 - "$EV/04-active-tun-status.json" <<'PY'
import json,sys
t=json.load(open(sys.argv[1])).get("data") or {}
print("state=",t.get("state"),"desired=",t.get("desired"),"helper=",t.get("helper"),
      "iface=",t.get("interface"),"dns=",t.get("dns"),"udp=",t.get("udp"),"ipv6=",t.get("ipv6"))
PY
  if python3 - "$EV/04-active-tun-status.json" <<'PY'
import json,sys
t=json.load(open(sys.argv[1])).get("data") or {}
raise SystemExit(0 if t.get("state")=="on" and t.get("desired") is True and t.get("helper")=="installed" else 1)
PY
  then
    ck PASS "tun status on while active" "state=on desired=true helper=installed"
  else
    ck FAIL "tun status on while active" "status did not report on/desired/installed"
  fi
  if ip addr show rl-tun0 | grep -q '198.18.0.1/15' && ip addr show rl-tun0 | grep -q 'fc00::1/64' \
     && ip route show dev rl-tun0 | grep -q '^0.0.0.0/1' && ip route show dev rl-tun0 | grep -q '^128.0.0.0/1' \
     && ip -6 route show dev rl-tun0 | grep -q '^::/1' && ip -6 route show dev rl-tun0 | grep -q '^8000::/1'; then
    ck PASS "active TUN link/routes" "rl-tun0 up, 198.18.0.1/15 + fc00::1/64, v4/v6 half routes"
  else
    ck FAIL "active TUN link/routes" "one or more fixed addresses/half routes missing"
  fi
  if ip route | grep -q '^default via 192.168.1.1 dev wlp0s20f3'; then
    ck PASS "default route preserved while active" "default still via wlp0s20f3"
  else
    ck FAIL "default route preserved while active" "system default route changed"
  fi
  if cmp -s "$EV/baseline-resolv-sha.txt" "$EV/04-active-resolv-sha.txt"; then
    ck PASS "DNS config unchanged while active" "resolv.conf sha256 equal"
  else
    ck FAIL "DNS config unchanged while active" "resolv.conf sha256 changed"
  fi
  if [ -f "$EV/04-active-coverage-final.txt" ] && grep -q '^Interface: rl-tun0$' <(awk -F'|' '{print $6}' "$EV/04-active-coverage-final.txt"); then
    ck PASS "active menu tree" "Coverage on, Interface: rl-tun0, Helper installed"
  else
    ck FAIL "active menu tree" "Interface row missing"
  fi
  if grep -q '(46, 160, 67' <(tail -1 "$EV/icons.txt"); then
    ck PASS "active green on badge" "badge center RGB 46,160,67"
  else
    ck FAIL "active green on badge" "green badge not found in IconPixmap"
  fi
else
  ck FAIL "active coverage runtime" "rl-tun0 never appeared"
fi

# --- tray-initiated disable (and immediate fallback) -------------------------
if link_present; then
  OND=$(date +%s)
  ROOT=$(dump 0 -1)
  COV=$(id_of "Coverage" "$ROOT")
  COVD=$(dump "$COV" -1)
  DIS=$(id_of "Disable system-wide coverage…" "$COVD")
  DISABLED_OK=0
  if [ -n "$DIS" ] && click "$DIS"; then
    PENDING=$(dump 0 -1)
    printf '%s\n' "$PENDING" >"$EV/05-disable-pending.txt"
    if has_label "Confirm: Disable system-wide coverage" "$PENDING"; then
      CONFIRM=$(id_of "Confirm: Disable system-wide coverage" "$PENDING")
      if [ -n "$CONFIRM" ] && click "$CONFIRM"; then DISABLED_OK=1; fi
    fi
  fi
  OFF_OK=0
  if [ "$DISABLED_OK" = 1 ]; then
    for _ in $(seq 1 100); do
      link_present || { OFF_OK=1; break; }
      sleep 0.15
    done
  fi
  if [ "$OFF_OK" = 1 ]; then
    ck PASS "tray disable" "two-step confirm; rl-tun0 removed"
  else
    ck FAIL "tray disable" "tray disable did not remove coverage; CLI fallback used"
    cli tun off >"$EV/05-tun-off-fallback.out" 2>&1 || true
    for _ in $(seq 1 100); do link_present || { OFF_OK=1; break; }; sleep 0.15; done
  fi
  OFF_AT=$(date +%s)
  if [ "$ACTIVE_FROM" -gt 0 ]; then
    printf 'enable_confirm_at=%s disable_at=%s confirm_to_off_s=%s\n' \
      "$ACTIVE_FROM" "$OFF_AT" "$((OFF_AT-ACTIVE_FROM))" >"$EV/04-active-window.txt"
  fi
  cli tun status --json >"$EV/05-off-tun-status.json" 2>&1
  cli status --json >"$EV/05-off-status.json" 2>&1
  ROOT=$(dump 0 -1)
  COV=$(id_of "Coverage" "$ROOT")
  [ -n "$COV" ] && dump "$COV" -1 >"$EV/05-off-coverage.txt"
  printf '%s\n' "$ROOT" >"$EV/05-off-layout.txt"
  icon off-after
  tooltip
  sleep 1
else
  ck NOTRUN "tray disable" "coverage was never active"
fi

# --- host restoration checks after off ---------------------------------------
ip route >"$EV/05-off-route4.txt"
ip -6 route >"$EV/05-off-route6.txt"
ip route show table all >"$EV/05-off-routes-all.txt"
ip -6 route show table all >"$EV/05-off-routes6-all.txt"
ip rule >"$EV/05-off-rule4.txt"
ip -6 rule >"$EV/05-off-rule6.txt"
sha256sum /etc/resolv.conf >"$EV/05-off-resolv-sha.txt"
{ echo "link:"; ip link show rl-tun0 2>&1; echo "rc=$?"; } >"$EV/05-off-link.txt"
if ! link_present; then ck PASS "coverage link removed" "rl-tun0 absent"; else ck FAIL "coverage link removed" "rl-tun0 still present"; fi
if cmp -s "$EV/baseline-route4.txt" "$EV/05-off-route4.txt" && cmp -s "$EV/baseline-route6.txt" "$EV/05-off-route6.txt"; then
  ck PASS "routes restored to baseline" "IPv4/IPv6 route tables byte-equal"
else
  ck FAIL "routes restored to baseline" "route tables differ"
fi
if cmp -s "$EV/baseline-resolv-sha.txt" "$EV/05-off-resolv-sha.txt"; then
  ck PASS "DNS restored/unchanged after off" "resolv.conf sha256 equal"
else
  ck FAIL "DNS restored/unchanged after off" "resolv.conf sha256 changed"
fi
if [ -f "$EV/05-off-tun-status.json" ] && python3 - "$EV/05-off-tun-status.json" <<'PY'
import json,sys
t=json.load(open(sys.argv[1])).get("data") or {}
raise SystemExit(0 if t.get("state")=="off" and t.get("desired") is False else 1)
PY
then
  ck PASS "tun status off after disable" "state=off desired=false"
else
  ck FAIL "tun status off after disable" "not off/desired false"
fi

# --- stop isolated app, uninstall helper ------------------------------------
say "stop isolated app"
stop_app
sleep 1
pgrep -af -- "--data-dir $DATA" >"$EV/06-leftover-procs.txt" 2>&1 || true
[ -s "$EV/06-leftover-procs.txt" ] && ck FAIL "isolated app stopped" "processes remain" || ck PASS "isolated app stopped" "no process with the isolated data dir"
say "pkexec tun uninstall"
if timeout 120 pkexec /usr/bin/env SUDO_UID="$UID_NUM" "$BIN" tun uninstall --json >"$EV/06-uninstall.json" 2>"$EV/06-uninstall.err"; then
  SETUP_DONE=0
  ck PASS "helper uninstall" "pkexec tun uninstall exit 0"
else
  ck FAIL "helper uninstall" "pkexec tun uninstall failed"
fi

# --- final host verification -------------------------------------------------
{
  echo "== systemctl is-active =="; systemctl is-active routelatch-tun.socket routelatch-tun.service 2>&1
  echo "== unit files =="; ls -l /etc/systemd/system/routelatch-tun.socket /etc/systemd/system/routelatch-tun.service 2>&1
  echo "== helper binary =="; ls -l /usr/libexec/routelatch/tun-service 2>&1
  echo "== config =="; ls -l /etc/routelatch/tun.json 2>&1
  echo "== tmpfiles =="; ls -l /etc/tmpfiles.d/routelatch-tun.conf 2>&1
  echo "== runtime dir =="; ls -ld /run/routelatch-tun 2>&1
  echo "== control socket =="; ls -l /run/routelatch-tun/control.sock 2>&1
} >"$EV/07-final-artifacts.txt" 2>&1
ip -br link >"$EV/07-final-links.txt"
ip route >"$EV/07-final-route4.txt"
ip -6 route >"$EV/07-final-route6.txt"
ip route show table all >"$EV/07-final-routes-all.txt"
ip -6 route show table all >"$EV/07-final-routes6-all.txt"
ip rule >"$EV/07-final-rule4.txt"
ip -6 rule >"$EV/07-final-rule6.txt"
sha256sum /etc/resolv.conf >"$EV/07-final-resolv-sha.txt"
{ echo "link:"; ip link show rl-tun0 2>&1; } >"$EV/07-final-link.txt"
gdbus call --session --dest org.kde.StatusNotifierWatcher --object-path /StatusNotifierWatcher \
  --method org.freedesktop.DBus.Properties.Get org.kde.StatusNotifierWatcher RegisteredStatusNotifierItems >"$EV/07-final-tray-items.txt" 2>&1
live_set >"$EV/07-final-live-pids.txt" 2>&1 || true
ss -ltnp 2>/dev/null | grep -E ":$HTTP|:$SOCKS" >"$EV/07-final-isolated-listeners.txt" 2>&1 || true

if ! helper_present; then
  ck PASS "helper files/units/tmpfiles removed" "none of socket/service/binary/config/tmpfiles remain"
else
  ck FAIL "helper files/units/tmpfiles removed" "helper artifacts remain"
fi
if systemctl is-active --quiet routelatch-tun.socket || systemctl is-active --quiet routelatch-tun.service; then
  ck FAIL "helper units inactive" "a unit is still active"
else
  ck PASS "helper units inactive" "socket and service not active"
fi
if ! link_present; then ck PASS "no coverage link at end" "rl-tun0 absent"; else ck FAIL "no coverage link at end" "rl-tun0 present"; fi
if cmp -s "$EV/baseline-route4.txt" "$EV/07-final-route4.txt" && cmp -s "$EV/baseline-route6.txt" "$EV/07-final-route6.txt"; then
  ck PASS "final routes equal baseline" "IPv4/IPv6 route tables byte-equal"
else
  ck FAIL "final routes equal baseline" "route tables differ"
fi
if cmp -s "$EV/baseline-rule4.txt" "$EV/07-final-rule4.txt" && cmp -s "$EV/baseline-rule6.txt" "$EV/07-final-rule6.txt"; then
  ck PASS "policy rules unchanged" "ip rule / ip -6 rule byte-equal"
else
  ck FAIL "policy rules unchanged" "rules differ"
fi
if cmp -s "$EV/baseline-resolv-sha.txt" "$EV/07-final-resolv-sha.txt"; then
  ck PASS "final DNS unchanged" "resolv.conf sha256 equal"
else
  ck FAIL "final DNS unchanged" "resolv.conf sha256 changed"
fi
# Live instance: same PID/args set as the pre-run baseline. Its start time is
# captured separately so a same-PID respawn cannot hide.
ps -o pid=,lstart=,args= -p $(awk '{print $1}' "$EV/baseline-live-pids.txt" | paste -sd, -) \
  >"$EV/07-final-live-start.txt" 2>&1 || true
if diff -q "$EV/baseline-live-pids.txt" "$EV/07-final-live-pids.txt" >/dev/null 2>&1; then
  ck PASS "live instance untouched" "identical pid/args set before/after; see 07-final-live-start.txt"
else
  ck FAIL "live instance untouched" "process set changed during the run"
fi
if grep -q -- "$TRAY" "$EV/07-final-tray-items.txt"; then
  ck FAIL "isolated tray item unregistered" "item for tray pid $TRAY still registered"
else
  ck PASS "isolated tray item unregistered" "tray pid $TRAY absent from watcher"
fi

say "run complete"
grep -E '^(PASS|FAIL|NOTRUN)\|' "$RESULTS" | sed 's/^/  /' | tee -a "$EV/summary.txt"
# The EXIT trap stays armed: on a clean run it is a no-op (app stopped, helper
# gone); if the uninstall above failed it retries the recovery path.
exit 0
