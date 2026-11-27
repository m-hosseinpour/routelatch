#!/usr/bin/env bash
# R16-H tray acceptance, part 1: the real product engine on an isolated data
# dir with no TUN helper installed. No host TUN, service, autostart or desktop
# setting is touched; the temporary tray is the product's own tray child.
set -euo pipefail

REPO=${REPO:-<repo>}  # set REPO to the repository root
WORK=${WORK:-${TMPDIR:-/tmp}/routelatch-h-tray}  # scratch root
BIN=${BIN:-$WORK/bin/routelatch}
TS=$(date -u +%Y%m%dT%H%M%SZ)
BASE=${BASE:-$WORK/real-$TS}
DATA=$BASE/data
XDG=$BASE/xdg
EV=${EV:-$REPO/spike/tun/evidence/h-tray-2026-11-27/real}
HELPER=${HELPER:-$REPO/spike/tun/evidence/h-tray-2026-11-27/menu-dump2.py}
mkdir -p "$DATA" "$XDG/config" "$XDG/state" "$XDG/home" "$XDG/cache" "$EV"
chmod 700 "$BASE" "$DATA" "$XDG"/*

export DISPLAY=${DISPLAY:-:0}
export DBUS_SESSION_BUS_ADDRESS=${DBUS_SESSION_BUS_ADDRESS:-unix:path=${XDG_RUNTIME_DIR:-/run/user/$(id -u)}/bus}
export XDG_CONFIG_HOME=$XDG/config XDG_STATE_HOME=$XDG/state HOME=$XDG/home
export XDG_CACHE_HOME=$XDG/cache XDG_CURRENT_DESKTOP=KDE

say() { printf '%s %s\n' "$(date -u +%H:%M:%S)" "$*" | tee -a "$EV/steps.txt"; }
fail() { say "FAIL: $*"; exit 1; }
cli() { "$BIN" --data-dir "$DATA" "$@"; }
dump() { python3 "$HELPER" "$BUS" "$1" "$2"; }
id_of() { awk -F'|' -v l="$1" '$6==l {print $1; exit}' <<<"$2"; }
has_label() { awk -F'|' -v l="$1" '$6==l {found=1} END{exit !found}' <<<"$2"; }
has_prefix() { awk -F'|' -v p="$1" 'index($6,p)==1 {found=1} END{exit !found}' <<<"$2"; }
click() {
  local id=$1
  [ -n "$id" ] || fail "click with empty id"
  say "click id=$id"
  gdbus call --session --dest "$BUS" --object-path /StatusNotifierMenu --method com.canonical.dbusmenu.Event "$id" clicked "<''>" 0 >/dev/null
}
header() { dump 0 -1 | awk -F'|' 'NR>1 && $6!="" {print $6; exit}'; }
wait_header() {
  local want=$1 tries=${2:-60}
  for _ in $(seq 1 "$tries"); do
    [ "$(header)" = "$want" ] && return 0
    sleep 0.1
  done
  fail "header never became '$want' (now '$(header)')"
}
wait_label() {
  local label=$1 tries=${2:-100}
  for _ in $(seq 1 "$tries"); do
    has_label "$label" "$(dump 0 -1)" && return 0
    sleep 0.1
  done
  fail "label '$label' never appeared"
}
wait_no_label() {
  local label=$1 tries=${2:-100}
  for _ in $(seq 1 "$tries"); do
    has_label "$label" "$(dump 0 -1)" || return 0
    sleep 0.1
  done
  fail "label '$label' never disappeared"
}
icon() {
  local name=$1
  gdbus call --session --dest "$BUS" --object-path /StatusNotifierItem \
    --method org.freedesktop.DBus.Properties.Get org.kde.StatusNotifierItem IconPixmap \
    | python3 "$REPO/spike/tun/evidence/h-tray-2026-11-27/icon-dump.py" "$EV/icon-$name.png" \
    | tee -a "$EV/icons.txt"
}
tooltip() {
  gdbus call --session --dest "$BUS" --object-path /StatusNotifierItem \
    --method org.freedesktop.DBus.Properties.Get org.kde.StatusNotifierItem ToolTip \
    | tee -a "$EV/tooltips.txt"
}
sup_pid() { python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["pid"])' "$DATA/run/app.owner"; }

cleanup() {
  kill "${NMON:-}" 2>/dev/null || true
  if [ -f "$DATA/run/app.owner" ]; then
    local sup
    sup=$(sup_pid 2>/dev/null || true)
    [ -n "$sup" ] && kill -TERM "$sup" 2>/dev/null || true
  fi
  sleep 1
  for pid in $(pgrep -f -- "$BIN --data-dir $DATA" || true); do kill -9 "$pid" 2>/dev/null || true; done
}
trap cleanup EXIT INT TERM

dbus-monitor "interface='org.freedesktop.Notifications'" >"$EV/notifications.log" 2>&1 &
NMON=$!
sleep 0.5

read -r HTTP SOCKS < <(python3 - <<'PY'
import socket
p=[]
for _ in range(2):
    s=socket.socket(); s.bind(("127.0.0.1",0)); p.append(s.getsockname()[1]); s.close()
print(p[0],p[1])
PY
)
printf '{"schema_version":1,"http_listen":"127.0.0.1:%s","socks_listen":"127.0.0.1:%s","log_level":"info"}\n' "$HTTP" "$SOCKS" >"$DATA/config.json"
chmod 600 "$DATA/config.json"

say "base=$BASE http=$HTTP socks=$SOCKS"
"$BIN" --data-dir "$DATA" run >"$EV/run.out" 2>&1 || fail "run failed: $(cat "$EV/run.out")"
cli status --json >"$EV/status-0-real.json"
python3 - "$EV/status-0-real.json" <<'PY'
import json,sys
d=json.load(open(sys.argv[1]))["data"]
print("status:", d["mode"], "ready=", d["ready"], "tun=", json.dumps(d.get("tun")))
PY

TRAY=$(grep -ho 'tray_started pid=[0-9]*' "$DATA"/log/*.log 2>/dev/null | tail -1 | cut -d= -f2 || true)
[ -n "${TRAY:-}" ] || fail "no tray_started in log"
BUS=""
for _ in $(seq 1 100); do
  LIST=$(gdbus call --session --dest org.kde.StatusNotifierWatcher --object-path /StatusNotifierWatcher --method org.freedesktop.DBus.Properties.Get org.kde.StatusNotifierWatcher RegisteredStatusNotifierItems)
  BUS=$(echo "$LIST" | grep -o "org.freedesktop.StatusNotifierItem-$TRAY-[0-9]*" | head -1 || true)
  [ -n "$BUS" ] && break
  sleep 0.1
done
[ -n "$BUS" ] || fail "tray item for pid $TRAY not registered"
SUP=$(sup_pid)
printf 'base=%s\ndata=%s\nhttp=%s\nsocks=%s\nsupervisor=%s\ntray=%s\nbus=%s\nbinary=%s\nhead=%s\n' \
  "$BASE" "$DATA" "$HTTP" "$SOCKS" "$SUP" "$TRAY" "$BUS" "$BIN" "$(git -C "$REPO" rev-parse HEAD)" >"$EV/run-env.txt"
say "tray=$TRAY bus=$BUS supervisor=$SUP"
wait_header "Targeted · 0 usable · 0 open"

# Coverage as the real engine reports it with no helper installed.
ROOT=$(dump 0 -1)
COV=$(id_of "Coverage" "$ROOT")
[ -n "$COV" ] || fail "no Coverage submenu"
COVD=$(dump "$COV" -1)
printf '%s\n' "$COVD" >"$EV/coverage-off-missing.txt"
say "coverage(off/missing) captured"
icon off-real
tooltip
printf '%s\n' "$(dump 0 -1)" >"$EV/layout-real-off.txt"

# Clicking a disabled Enable must create no pending and no request.
EN=$(id_of "Enable system-wide coverage…" "$COVD")
[ -n "$EN" ] || fail "no Enable item"
click "$EN"
sleep 1
AFTER=$(dump 0 -1)
has_prefix "Confirm:" "$AFTER" && fail "disabled Enable created a pending"
has_prefix "Job:" "$AFTER" && fail "disabled Enable started a job"
printf '%s\n' "$AFTER" >"$EV/layout-after-disabled-enable-click.txt"
grep -h 'click rejected' "$DATA"/log/*.log | tail -1 >"$EV/tray-click-rejected.txt" || true
say "disabled Enable click: no pending, no job"

# The real CLI records the wish (helper still missing) -> desired on, not active.
set +e
cli tun on >"$EV/tun-on-cli.out" 2>"$EV/tun-on-cli.err"
RC=$?
set -e
printf 'exit=%s\n' "$RC" >>"$EV/tun-on-cli.out"
cli status --json >"$EV/status-1-desired.json"
python3 - "$EV/status-1-desired.json" <<'PY'
import json,sys
d=json.load(open(sys.argv[1]))["data"]
print("status:", d["mode"], "tun=", json.dumps(d.get("tun")), "warnings=", d.get("warnings"))
PY
COVD=""
for _ in $(seq 1 100); do
  COVD=$(dump "$COV" -1)
  has_prefix "Coverage: " "$COVD" && grep -q 'desired on' <<<"$COVD" && break
  sleep 0.1
done
grep -q 'desired on' <<<"$COVD" || fail "coverage did not report desired on: $COVD"
printf '%s\n' "$COVD" >"$EV/coverage-desired-on-missing.txt"
icon error-desired-missing
tooltip
printf '%s\n' "$(dump 0 -1)" >"$EV/layout-real-desired-on.txt"
say "desired on with missing helper: state and icon captured"

# Real two-step disable: coverage reduction must confirm first.
DIS=$(id_of "Disable system-wide coverage…" "$COVD")
[ -n "$DIS" ] || fail "no enabled Disable item"
click "$DIS"
PENDING=$(dump 0 -1)
printf '%s\n' "$PENDING" >"$EV/layout-disable-pending.txt"
has_label "Confirm: Disable system-wide coverage" "$PENDING" || fail "no disable confirmation"
has_label "Cancel" "$PENDING" || fail "no Cancel next to the confirmation"
say "disable pending shown"
CONFIRM=$(id_of "Confirm: Disable system-wide coverage" "$PENDING")
[ -n "$CONFIRM" ] || fail "no confirm id"
click "$CONFIRM"
for _ in $(seq 1 100); do
  has_prefix "Confirm:" "$(dump 0 -1)" || break
  sleep 0.1
done
cli status --json >"$EV/status-2-off.json"
python3 - "$EV/status-2-off.json" <<'PY'
import json,sys
d=json.load(open(sys.argv[1]))["data"]
print("status:", d["mode"], "tun=", json.dumps(d.get("tun")))
PY
wait_label "Coverage: off · desired off" 100
COVD=$(dump "$COV" -1)
printf '%s\n' "$COVD" >"$EV/coverage-off-desired-off.txt"
icon off-desired
printf '%s\n' "$(dump 0 -1)" >"$EV/layout-real-off-desired.txt"
say "disable confirmed: wish cleared, off"

# Notifications must not carry a destination/URL.
sleep 1
python3 "$REPO/spike/tun/evidence/h-tray-2026-11-27/notify-summary.py" \
  "$EV/notifications.log" >"$EV/notifications-summary.txt"
if grep -Eqi 'https?://|@|:[0-9]{2,5}/' "$EV/notifications.log"; then
  say "NOTIFY SCAN: URL-like text present"; else say "notify scan: no URL/destination"; fi

say "real run complete; tray=$TRAY"
