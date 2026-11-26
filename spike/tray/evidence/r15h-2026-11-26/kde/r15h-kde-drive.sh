#!/usr/bin/env bash
# R15-H live acceptance: product tray child on the real Plasma/X11 session.
# Isolated data dir, XDG bases, ports and a fake konsole. No desktop-proxy
# set/restore, no autostart/service install, no installed-service interaction.
set -euo pipefail

REPO=${REPO:-/path/to/routelatch}
BIN=${BIN:-/tmp/opencode/r15h/dist-pkg/routelatch-linux-amd64}
TS=$(date -u +%Y%m%dT%H%M%SZ)
BASE=/tmp/opencode/r15h/kde-$TS
DATA=$BASE/data
XDG=$BASE/xdg
EV=$BASE/evidence
HELPER=/tmp/opencode/r15h/menu-dump.py
CANARY="rl-canary-$(head -c 8 /dev/urandom | od -An -tx1 | tr -d ' \n').invalid"
mkdir -p "$DATA" "$XDG/config" "$XDG/state" "$XDG/home" "$XDG/cache" "$XDG/bin" "$EV"
chmod 700 "$BASE" "$DATA" "$XDG"/*

export DISPLAY=:0
export DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus
export XDG_CONFIG_HOME=$XDG/config XDG_STATE_HOME=$XDG/state HOME=$XDG/home
export XDG_CACHE_HOME=$XDG/cache XDG_CURRENT_DESKTOP=KDE
export PATH="$XDG/bin:$PATH"

cat >"$XDG/bin/konsole" <<EOF
#!/bin/sh
printf '%s\n' "\$@" >>"$XDG/terminal-argv.log"
EOF
chmod 755 "$XDG/bin/konsole"

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

say() { printf '%s %s\n' "$(date -u +%H:%M:%S)" "$*" | tee -a "$EV/steps.txt"; }
fail() {
  say "FAIL: $*"
  dump 0 10 >"$EV/failed-layout.txt" 2>/dev/null || true
  exit 1
}
cli() { "$BIN" --data-dir "$DATA" "$@"; }
gl() { gdbus call --session --dest "$BUS" --object-path /StatusNotifierMenu --method com.canonical.dbusmenu.GetLayout "$1" "$2" "[]"; }
dump() { gl "$1" "$2" | python3 "$HELPER"; }
id_of() { awk -F'|' -v l="$1" '$6==l {print $1; exit}' <<<"$2"; }
id_prefix() { awk -F'|' -v p="$1" 'index($6,p)==1 {print $1; exit}' <<<"$2"; }
has_label() { awk -F'|' -v l="$1" '$6==l {found=1} END{exit !found}' <<<"$2"; }
has_prefix() { awk -F'|' -v p="$1" 'index($6,p)==1 {found=1} END{exit !found}' <<<"$2"; }
click() {
  local id=$1
  [ -n "$id" ] || fail "click with empty id"
  say "click id=$id"
  gdbus call --session --dest "$BUS" --object-path /StatusNotifierMenu --method com.canonical.dbusmenu.Event "$id" clicked "<''>" 0 >/dev/null
}
header() { dump 0 10 | awk -F'|' 'NR>1 && $6!="" {print $6; exit}'; }
wait_header() {
  local want=$1 tries=${2:-40}
  for _ in $(seq 1 "$tries"); do
    [ "$(header)" = "$want" ] && return 0
    sleep 0.1
  done
  fail "header never became '$want' (now '$(header)')"
}
wait_header_prefix() {
  local want=$1 tries=${2:-40}
  for _ in $(seq 1 "$tries"); do
    case "$(header)" in "$want"*) return 0;; esac
    sleep 0.1
  done
  fail "header never started with '$want' (now '$(header)')"
}
wait_label() {
  local label=$1 tries=${2:-60}
  for _ in $(seq 1 "$tries"); do
    has_label "$label" "$(dump 0 10)" && return 0
    sleep 0.2
  done
  fail "label '$label' never appeared"
}
wait_no_label() {
  local label=$1 tries=${2:-60}
  for _ in $(seq 1 "$tries"); do
    has_label "$label" "$(dump 0 10)" || return 0
    sleep 0.2
  done
  fail "label '$label' never disappeared"
}
wait_gone() {
  local tries=${1:-80}
  for _ in $(seq 1 "$tries"); do
    if ! pgrep -f -- "$BIN --data-dir $DATA" >/dev/null; then return 0; fi
    sleep 0.2
  done
  fail "a test process is still running"
}
sup_pid() { python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))["pid"])' "$DATA/run/app.owner"; }
engine_pid() { grep -ho 'engine_started pid=[0-9]*' "$DATA"/log/*.log | tail -1 | cut -d= -f2; }

cleanup() {
  kill "$NMON" 2>/dev/null || true
  if [ -n "${BUS:-}" ]; then :; fi
  local sup
  if [ -f "$DATA/run/app.owner" ]; then
    sup=$(sup_pid 2>/dev/null || true)
    [ -n "$sup" ] && kill -TERM "$sup" 2>/dev/null || true
  fi
  sleep 1
  for pid in $(pgrep -f -- "$BIN --data-dir $DATA" || true); do kill -9 "$pid" 2>/dev/null || true; done
}
trap cleanup EXIT INT TERM

dnsmon() { :; }
dbus-monitor "interface='org.freedesktop.Notifications'" >"$EV/notifications.log" 2>&1 &
NMON=$!
sleep 0.5

say "data=$DATA canary=$CANARY ports=$HTTP/$SOCKS binary=$BIN"
env XDG_CONFIG_HOME=$XDG_CONFIG_HOME XDG_STATE_HOME=$XDG_STATE_HOME HOME=$HOME \
  XDG_CACHE_HOME=$XDG_CACHE_HOME XDG_CURRENT_DESKTOP=KDE PATH=$PATH \
  "$BIN" --data-dir "$DATA" run >"$EV/run.out" 2>&1 || fail "run failed: $(cat "$EV/run.out")"
cli status --json >"$EV/status-ready.json"
for _ in $(seq 1 100); do
  TRAY=$(grep -ho 'tray_started pid=[0-9]*' "$DATA"/log/*.log 2>/dev/null | tail -1 | cut -d= -f2 || true)
  [ -n "${TRAY:-}" ] && break
  sleep 0.1
done
[ -n "${TRAY:-}" ] || fail "no tray_started in log"
for _ in $(seq 1 100); do
  LIST=$(gdbus call --session --dest org.kde.StatusNotifierWatcher --object-path /StatusNotifierWatcher --method org.freedesktop.DBus.Properties.Get org.kde.StatusNotifierWatcher RegisteredStatusNotifierItems)
  BUS=$(echo "$LIST" | grep -o "org.freedesktop.StatusNotifierItem-$TRAY-[0-9]*" | head -1 || true)
  [ -n "${BUS:-}" ] && break
  sleep 0.1
done
[ -n "${BUS:-}" ] || fail "tray item for pid $TRAY not registered"
SUP=$(sup_pid)
printf 'base=%s\ndata=%s\nxdg=%s\ncanary=%s\nhttp=%s\nsocks=%s\nsupervisor=%s\ntray=%s\nbus=%s\n' \
  "$BASE" "$DATA" "$XDG" "$CANARY" "$HTTP" "$SOCKS" "$SUP" "$TRAY" "$BUS" >"$EV/run-env.txt"
say "supervisor=$SUP tray=$TRAY bus=$BUS"

# --- fixtures: 25 rules and 25 pool records, with hostile and long labels ---
for i in $(seq -w 1 25); do cli rules add "r15h-page-$i.example" >/dev/null; done
python3 - "$BASE/links.txt" <<'PY'
import sys, uuid
out=[]
for i in range(1,26):
    name=f"r15h-pool-{i:02d}"
    if i == 23:
        name="r15h-%1B%5B31mred"
    elif i == 24:
        name="r15h-long-" + "A"*140
    uid=str(uuid.uuid5(uuid.NAMESPACE_DNS, name))
    out.append(f"vless://{uid}@127.0.0.1:{20000+i}?encryption=none&security=tls&sni=example.com&type=tcp#{name}")
open(sys.argv[1],"w").write("\n".join(out)+"\n")
PY
cli import file "$BASE/links.txt" >"$EV/import.json" || fail "pool import failed"
cli pool list --limit 1 --json >"$EV/pool-counts.json"
python3 - "$EV/pool-counts.json" <<'PY' || exit 1
import json,sys
d=json.load(open(sys.argv[1]))["data"]
c=d["counts"]
assert c["total"]==25, c
print("pool counts", c)
PY
wait_label "Approved rules (25)" 100
wait_label "Proposals (0)" 100
say "fixtures loaded"

# --- initial surface ---
LAY=$(dump 0 10)
printf '%s\n' "$LAY" >"$EV/layout-initial.txt"
case "$(header)" in "Targeted · "*) ;; *) fail "initial header '$(header)'";; esac
TARGETED=$(id_of "Targeted" "$LAY")
grep -q "^$TARGETED|||radio|1|" <<<"$LAY" || fail "Targeted is not the checked radio"
for label in "Mode" "Proposals (0)" "Approved rules (25)" "Pool" "Desktop proxy" "Diagnostics" "Data" "Settings" "Advanced" "Quit"; do
  has_label "$label" "$LAY" || fail "missing root submenu '$label'"
done
has_prefix "Last action:" "$LAY" || fail "missing Last action row"
say "initial surface: header, radio, root submenus present"

# --- rules pagination ---
RULES_ID=$(id_of "Approved rules (25)" "$LAY")
RD=$(dump "$RULES_ID" 1)
printf '%s\n' "$RD" >"$EV/rules-page1.txt"
has_label "Page 1/2" "$RD" || fail "rules page 1 marker missing"
NEXT=$(id_of "Next page" "$RD")
[ -n "$NEXT" ] || fail "rules Next page missing"
click "$NEXT"
for _ in $(seq 1 40); do RD=$(dump "$RULES_ID" 1); has_label "Page 2/2" "$RD" && break; sleep 0.1; done
has_label "Page 2/2" "$RD" || fail "rules did not advance to page 2"
has_prefix "r15h-page-25.example" "$RD" || fail "page 2 does not show the last rule"
printf '%s\n' "$RD" >"$EV/rules-page2.txt"
say "rules pagination 1/2 -> 2/2"

# --- pool groups, pagination and sanitization ---
LAY=$(dump 0 10)
UNK=$(awk -F'|' '$6 ~ /^Untested \(/ {print $6}' <<<"$LAY" | head -1)
[ -n "$UNK" ] || fail "Untested pool group missing"
UNK_ID=$(id_of "$UNK" "$LAY")
PD=$(dump "$UNK_ID" 1)
printf '%s\n' "$PD" >"$EV/pool-page1.txt"
has_label "Page 1/2" "$PD" || fail "pool page 1 marker missing"
PNEXT=$(id_of "Next page" "$PD")
click "$PNEXT"
for _ in $(seq 1 40); do PD=$(dump "$UNK_ID" 1); has_label "Page 2/2" "$PD" && break; sleep 0.1; done
has_label "Page 2/2" "$PD" || fail "pool did not advance to page 2"
printf '%s\n' "$PD" >"$EV/pool-page2.txt"
RAW=$(gl 0 10)
if printf '%s' "$RAW" | LC_ALL=C grep -q $'\x1b'; then fail "ESC reached the D-Bus layout"; fi
LONG=$(awk -F'|' 'index($6,"r15h-long-")==1 {print $6}' <<<"$PD")
[ -n "$LONG" ] || fail "long pool label missing"
python3 - "$LONG" <<'PY' || exit 1
import sys
label=sys.argv[1]
assert len(label) <= 81, len(label)
assert label.endswith("…"), label[-10:]
print("long label length", len(label))
PY
say "pool pagination and label sanitization/truncation verified"

# --- one offline job: Refresh all sources ---
LAY=$(dump 0 10)
POOL_ID=$(id_of "Pool" "$LAY")
POOLD=$(dump "$POOL_ID" 1)
REFRESH=$(id_of "Refresh all sources" "$POOLD")
click "$REFRESH"
wait_no_label "Job: pool refresh running" 100
sleep 2
dump 0 10 >"$EV/layout-after-refresh.txt"
say "pool refresh job exercised: $(awk -F'|' 'index($6,"Last action:")==1 {print $6}' "$EV/layout-after-refresh.txt" | head -1)"

# --- desktop status is read-only ---
LAY=$(dump 0 10)
DESK_ID=$(id_of "Desktop proxy" "$LAY")
DESK=$(dump "$DESK_ID" 1)
printf '%s\n' "$DESK" >"$EV/desktop.txt"
has_prefix "Desktop proxy:" "$DESK" || fail "desktop status row missing"
has_label "Refresh status" "$DESK" || fail "desktop Refresh missing"
has_label "Set/Re-apply desktop proxy…" "$DESK" || fail "desktop Set missing"
has_label "Restore previous desktop proxy…" "$DESK" || fail "desktop Restore missing"
DREFRESH=$(id_of "Refresh status" "$DESK")
click "$DREFRESH"
sleep 2
dump "$DESK_ID" 1 >"$EV/desktop-after-refresh.txt"
say "desktop status read-only refresh; set/restore present but not clicked"

# --- diagnostics: doctor, log path notification, environment ---
LAY=$(dump 0 10)
DIAG_ID=$(id_of "Diagnostics" "$LAY")
DIAG=$(dump "$DIAG_ID" 1)
DOCTOR=$(id_of "Run doctor" "$DIAG")
click "$DOCTOR"
RES=""
for _ in $(seq 1 150); do
  DIAG=$(dump "$DIAG_ID" 1)
  RESID=$(id_of "Doctor results" "$DIAG")
  [ -n "$RESID" ] || break
  RES=$(dump "$RESID" 1)
  has_prefix "[ok]" "$RES" && break
  has_prefix "[warn]" "$RES" && break
  has_prefix "[error]" "$RES" && break
  sleep 0.2
done
printf '%s\n' "$RES" >"$EV/diagnostics.txt"
has_prefix "[ok]" "$RES" || has_prefix "[warn]" "$RES" || has_prefix "[error]" "$RES" || fail "doctor produced no result row"
ENVID=$(id_of "Environment" "$DIAG")
dump "$ENVID" 1 >"$EV/diagnostics-env.txt"
LOGITEM=$(id_of "Show log path" "$DIAG")
click "$LOGITEM"
sleep 1
say "doctor results and log-path notification exercised"

# --- data backup ---
LAY=$(dump 0 10)
DATA_ID=$(id_of "Data" "$LAY")
DATAD=$(dump "$DATA_ID" 1)
BACKUP=$(id_of "Back up state now" "$DATAD")
BEFORE=$( (find "$DATA/backup" -maxdepth 1 -name '*.json' 2>/dev/null || true) | wc -l)
click "$BACKUP"
AFTER=$BEFORE
for _ in $(seq 1 100); do
  AFTER=$( (find "$DATA/backup" -maxdepth 1 -name '*.json' 2>/dev/null || true) | wc -l)
  [ "$AFTER" -gt "$BEFORE" ] && break
  sleep 0.2
done
[ "$AFTER" -gt "$BEFORE" ] || fail "backup produced no file"
BACKUPFILE=$(find "$DATA/backup" -maxdepth 1 -name '*.json' -printf '%T@ %p\n' | sort -n | tail -1 | sed 's/^[^ ]* //')
find "$DATA" -type f >"$EV/files-after-backup.txt"
say "isolated backup written: $BACKUPFILE"

# --- settings: custom data dir disables autostart, paths/config visible ---
LAY=$(dump 0 10)
SET_ID=$(id_of "Settings" "$LAY")
SETD=$(dump "$SET_ID" 1)
printf '%s\n' "$SETD" >"$EV/settings.txt"
has_prefix "Start at login: " "$SETD" || fail "autostart row missing"
has_prefix "Start at login unavailable: autostart manages only the default data directory" "$SETD" \
  || fail "autostart did not expose the custom-data-dir reason"
has_prefix "HTTP 127.0.0.1:$HTTP · SOCKS 127.0.0.1:$SOCKS · log info" "$SETD" || fail "config summary missing"
has_prefix "State $DATA/state.json · config " "$SETD" || fail "paths row missing"
say "settings/autostart disabled state and data paths verified (isolated XDG)"

# --- terminal menu uses only the fake allowlisted konsole ---
LAY=$(dump 0 10)
ADV_ID=$(id_of "Advanced" "$LAY")
ADV=$(dump "$ADV_ID" 1)
TERM=$(id_of "Terminal menu…" "$ADV")
click "$TERM"
for _ in $(seq 1 50); do [ -s "$XDG/terminal-argv.log" ] && break; sleep 0.1; done
[ -s "$XDG/terminal-argv.log" ] || fail "fake konsole was not started"
grep -Fxq -- "-e" "$XDG/terminal-argv.log" || fail "konsole argv lost -e"
grep -Fxq -- "$BIN" "$XDG/terminal-argv.log" || fail "konsole argv lost the executable"
grep -Fxq -- "$DATA" "$XDG/terminal-argv.log" || fail "konsole argv lost the data dir"
grep -Fxq -- "menu" "$XDG/terminal-argv.log" || fail "konsole argv lost menu"
cp "$XDG/terminal-argv.log" "$EV/terminal-argv.txt"
say "Terminal menu used the fake allowlisted konsole argv"

# --- restart engine: confirmation and restarting/ready events ---
LAY=$(dump 0 10)
ADV=$(dump "$ADV_ID" 1)
RESTART=$(id_of "Restart engine…" "$ADV")
click "$RESTART"
sleep 0.5
LAY=$(dump 0 10)
CONFIRM=$(id_prefix "Confirm: Restart engine" "$LAY")
[ -n "$CONFIRM" ] || fail "restart confirmation missing"
click "$CONFIRM"
saw_restarting=0
for _ in $(seq 1 100); do
  H=$(header)
  case "$H" in "Restarting engine…"|"Starting engine…") saw_restarting=1; printf '%s\n' "$H" >"$EV/restarting-header.txt"; break;; esac
  sleep 0.05
done
wait_header_prefix "Targeted · " 200
[ "$saw_restarting" = 1 ] || say "NOTE: restarting header window was not sampled (event evidence in log)"
grep -q "supervisor.engine_restarted" "$DATA"/log/*.log || fail "engine_restarted missing from supervisor log"
grep "supervisor.restart_requested\|supervisor.engine_started\|supervisor.engine_restarted" "$DATA"/log/*.log >"$EV/restart-log.txt"
say "restart completed after confirmation; supervisor events captured"

# --- unavailable and recovery (engine SIGSTOP; no state change) ---
EPID=$(engine_pid)
[ -n "$EPID" ] || fail "cannot find engine pid"
kill -STOP "$EPID"
for _ in $(seq 1 100); do
  H=$(header)
  [ "$H" = "Engine unavailable; retrying…" ] && break
  sleep 0.2
done
printf '%s\n' "$H" >"$EV/unavailable-header.txt"
[ "$H" = "Engine unavailable; retrying…" ] || fail "engine-unavailable state not shown (now '$H')"
kill -CONT "$EPID"
wait_header_prefix "Targeted · " 200
say "engine unavailable and recovery shown"

# --- stop, restart-to-resume, and mode resume ---
LAY=$(dump 0 10)
MODE_ID=$(id_of "Mode" "$LAY")
MODED=$(dump "$MODE_ID" 1)
STOP=$(id_of "Stop listening…" "$MODED")
click "$STOP"
sleep 0.5
LAY=$(dump 0 10)
SCONFIRM=$(id_prefix "Confirm: Stop listening" "$LAY")
[ -n "$SCONFIRM" ] || fail "stop confirmation missing"
click "$SCONFIRM"
wait_header_prefix "Stopped · listeners closed" 100
LAY=$(dump 0 10)
printf '%s\n' "$LAY" >"$EV/layout-stopped.txt"
has_label "Restart engine to resume…" "$LAY" || fail "restart-to-resume row missing in stopped"
MODE_ID=$(id_of "Mode" "$LAY")
MODED=$(dump "$MODE_ID" 1)
for label in "Targeted" "All proxy" "All direct" "Stop listening…"; do
  ID=$(id_of "$label" "$MODED")
  grep -q "^$ID|false|" <<<"$MODED" || fail "$label is not disabled in stopped"
done
cli status --json >"$EV/status-stopped.json"
python3 - "$EV/status-stopped.json" <<'PY' || exit 1
import json,sys
d=json.load(open(sys.argv[1]))["data"]
assert d["mode"]=="stopped" and d["ready"] is True and d["listeners_open"] is False, d
assert d["resume_requires_restart"] is True, d
PY
RESUME=$(id_of "Restart engine to resume…" "$LAY")
click "$RESUME"
sleep 0.5
LAY=$(dump 0 10)
RCONFIRM=$(id_prefix "Confirm: Restart engine" "$LAY")
[ -n "$RCONFIRM" ] || fail "resume restart confirmation missing"
click "$RCONFIRM"
wait_header "Stopped · choose a mode to resume" 200
LAY=$(dump 0 10)
MODE_ID=$(id_of "Mode" "$LAY")
MODED=$(dump "$MODE_ID" 1)
TID=$(id_of "Targeted" "$MODED")
printf '%s\n' "$MODED" >"$EV/mode-after-resume.txt"
TID_ENABLED=$(awk -F'|' -v id="$TID" '$1==id {print ($2=="false" ? "false" : "true")}' <<<"$MODED")
[ "$TID_ENABLED" = "true" ] || fail "Targeted is not enabled after resume"
click "$TID"
wait_header_prefix "Targeted · " 100
say "stopped, process restart and mode resume verified"

# --- proposal flow and D21 ---
sleep 1
CODE=$(curl -sS -o /dev/null -w '%{http_code}' --max-time 8 -x "http://127.0.0.1:$HTTP" "http://$CANARY:443/" || true)
say "canary direct request through the isolated listener: HTTP $CODE"
[ -n "$CODE" ] || fail "canary request returned nothing"
for _ in $(seq 1 100); do
  LAY=$(dump 0 10)
  has_label "Proposals (1)" "$LAY" && break
  has_prefix "Proposals (1)" "$LAY" && break
  sleep 0.2
done
has_prefix "Proposals (1)" "$LAY" || fail "proposal for the canary never appeared"
PROP_ID=$(id_prefix "Proposals (1)" "$LAY")
PROPD=$(dump "$PROP_ID" 1)
printf '%s\n' "$PROPD" >"$EV/proposals.txt"
CANPROP=$(id_prefix "route · $CANARY:443" "$PROPD")
[ -n "$CANPROP" ] || fail "canary proposal submenu missing"
CANPROPD=$(dump "$CANPROP" 1)
printf '%s\n' "$CANPROPD" >"$EV/proposal-canary.txt"
has_label "Accept" "$CANPROPD" || fail "proposal Accept missing"
has_label "Reject" "$CANPROPD" || fail "proposal Reject missing"
has_label "Recheck" "$CANPROPD" || fail "proposal Recheck missing"
if grep -rIl -- "$CANARY" "$DATA" "$XDG" >"$EV/d21-preaccept-files.txt"; then
  fail "unapproved canary persisted before acceptance: $(cat "$EV/d21-preaccept-files.txt")"
fi
say "canary proposal exists only in the live menu; no file/backup hit"
REJECT=$(id_of "Reject" "$CANPROPD")
click "$REJECT"
wait_label "Proposals (0)" 100
if grep -rIl -- "$CANARY" "$DATA" "$XDG" >"$EV/d21-postreject-files.txt"; then
  fail "rejected canary remained in a file"
fi
LAY=$(dump 0 10)
RULES_ID=$(id_of "Approved rules (25)" "$LAY")
RD=$(dump "$RULES_ID" 1)
if has_label "Page 1/2" "$RD"; then
  NEXT=$(id_of "Next page" "$RD")
  click "$NEXT"
  for _ in $(seq 1 40); do RD=$(dump "$RULES_ID" 1); has_label "Page 2/2" "$RD" && break; sleep 0.1; done
fi
printf '%s\n' "$RD" >"$EV/rules-for-removal.txt"
CANRULE=$(id_prefix "r15h-page-25.example · domain" "$RD")
[ -n "$CANRULE" ] || fail "page-2 rule missing for removal"
CRD=$(dump "$CANRULE" 1)
REMOVE=$(id_of "Remove…" "$CRD")
click "$REMOVE"
sleep 0.5
LAY=$(dump 0 10)
RC=$(id_prefix "Confirm: Remove rule" "$LAY")
[ -n "$RC" ] || fail "rule removal confirmation missing"
click "$RC"
wait_label "Approved rules (24)" 100
say "proposal rejected; confirmed rule removal exercised; canary never persisted"

# --- quit: plain row because desktop proxy is not applied ---
LAY=$(dump 0 10)
printf '%s\n' "$LAY" >"$EV/layout-final.txt"
has_label "Quit RouteLatch" "$LAY" || fail "plain Quit missing"
if has_prefix "Restore desktop proxy and quit" "$LAY" || has_prefix "Quit and keep desktop proxy" "$LAY"; then
  fail "desktop-restore quit rows appeared while desktop status is not applied"
fi
QUIT=$(id_of "Quit RouteLatch" "$LAY")
click "$QUIT"
wait_gone 100
LIST=$(gdbus call --session --dest org.kde.StatusNotifierWatcher --object-path /StatusNotifierWatcher --method org.freedesktop.DBus.Properties.Get org.kde.StatusNotifierWatcher RegisteredStatusNotifierItems)
if echo "$LIST" | grep -q "StatusNotifierItem-$TRAY-"; then fail "tray item still registered"; fi
grep -q "supervisor.quit" "$DATA"/log/*.log || fail "supervisor quit log missing"
say "Quit removed processes and the temporary tray item"

# --- notifications and final secret/D21 scan ---
kill "$NMON" 2>/dev/null || true; NMON=
sleep 0.5
cp "$DATA"/log/*.log "$EV/app.log" 2>/dev/null || true
grep -n "Notify" "$EV/notifications.log" >"$EV/notifications-summary.txt" || true
for needle in "$CANARY" "http://" "https://" "token=" "<home>"; do
  if grep -qF -- "$needle" "$EV/notifications.log"; then
    fail "notification body contains '$needle'"
  fi
done
if grep -rIl -- "$CANARY" "$DATA" "$XDG" >"$EV/d21-final-files.txt"; then
  fail "canary appears in a final data/XDG file: $(cat "$EV/d21-final-files.txt")"
fi
if grep -qF -- "$CANARY" "$DATA"/log/*.log; then fail "canary appears in the log"; fi
for needle in "http://" "https://" "<home>"; do
  if grep -qF -- "$needle" "$DATA"/log/*.log; then fail "log contains '$needle'"; fi
done
# The supervisor lifecycle log carries a numeric restart token (`token=1`).
# It is a sequence number, not a credential; any other token shape is a fail.
LOGTOKENS=$(grep -hoE 'token=[^ ,]+' "$DATA"/log/*.log | sort -u || true)
if printf '%s\n' "$LOGTOKENS" | grep -vE '^token=[0-9]+$' | grep -q .; then
  fail "log contains a non-numeric token: $LOGTOKENS"
fi
{
  echo "== D21: canary occurrences in isolated data/XDG (expect none) =="
  grep -rIn -- "$CANARY" "$DATA" "$XDG" || echo "none"
  echo "== D21: canary in notification bodies (expect none) =="
  grep -F -- "$CANARY" "$EV/notifications.log" || echo "none"
  echo "== token= strings in the log (numeric lifecycle sequence only) =="
  printf '%s\n' "${LOGTOKENS:-none}"
  echo "== http(s):// or private home paths in the log (expect none) =="
  grep -nE 'https?://|/home/' "$DATA"/log/*.log || echo "none"
} >"$EV/d21-final.txt"
say "D21/notification scan passed"

date -u +%Y-%m-%dT%H:%M:%SZ >"$EV/finished-at.txt"
say "RUN OK $BASE"
