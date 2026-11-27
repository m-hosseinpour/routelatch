#!/usr/bin/env bash
# Panel-contrast verification for the RouteLatch tray icon.
#
# Real run: the product engine + supervisor on an isolated data dir with no TUN
# helper; the supervisor starts the product tray child.
# Fake run: the unchanged product tray child against the visual-only fake
# management peer from the H-TRAY harness.
#
# Only tight icon crops are written to the evidence directory. Full panel
# captures stay under $W and are removed, as are all tray/peer processes.
set -euo pipefail

REPO=$(cd "$(dirname "$0")/../../../.." && pwd)
HERE=$REPO/spike/tun/evidence/h-tray-2026-11-27
EV=$REPO/spike/tun/evidence/panel-contrast-2026-11-27
W=${W:-${TMPDIR:-/tmp}/routelatch-panel-contrast}
BIN=${BIN:-$W/bin/routelatch}

export DISPLAY=${DISPLAY:-:0}
export DBUS_SESSION_BUS_ADDRESS=${DBUS_SESSION_BUS_ADDRESS:-unix:path=${XDG_RUNTIME_DIR:-/run/user/$(id -u)}/bus}
export XDG_CURRENT_DESKTOP=${XDG_CURRENT_DESKTOP:-KDE}
ORIG_HOME=${HOME:-}
ORIG_XDG_CONFIG=${XDG_CONFIG_HOME:-}
ORIG_XDG_STATE=${XDG_STATE_HOME:-}
ORIG_XDG_CACHE=${XDG_CACHE_HOME:-}

say() { printf '%s %s\n' "$(date -u +%H:%M:%S)" "$*" | tee -a "$EV/run.log"; }
fail() { say "FAIL: $*"; exit 1; }
redact() { sed "s|$W|<scratch>|g"; }

PANEL=""
LAUNCHER=""
RUNPID=""
cleanup() {
  trap - EXIT INT TERM
  if [ -n "$LAUNCHER" ]; then kill "$LAUNCHER" 2>/dev/null || true; wait "$LAUNCHER" 2>/dev/null || true; fi
  if [ -n "$RUNPID" ]; then kill -TERM "$RUNPID" 2>/dev/null || true; wait "$RUNPID" 2>/dev/null || true; fi
  # Only the temporary binary built for this run; the installed live instance
  # uses /usr/bin/routelatch and is never matched.
  pkill -9 -f -- "$BIN" 2>/dev/null || true
  sleep 1
  gdbus call --session --dest org.kde.StatusNotifierWatcher --object-path /StatusNotifierWatcher \
    --method org.freedesktop.DBus.Properties.Get org.kde.StatusNotifierWatcher RegisteredStatusNotifierItems \
    2>/dev/null | redact >"$EV/bus-after.txt" || true
  rm -rf "$W"
}
trap cleanup EXIT INT TERM

mkdir -p "$EV/real" "$EV/fake" "$W"
: >"$EV/run.log"
[ -x "$BIN" ] || fail "binary not found: $BIN"

# --- session ---------------------------------------------------------------
PANEL=$(python3 - <<'PY'
import re, subprocess
out = subprocess.check_output(["xwininfo", "-root", "-tree"], text=True)
best = None
for line in out.splitlines():
    m = re.search(r'(0x[0-9a-f]+) "plasmashell".*?\s(\d+)x(\d+)\+(-?\d+)\+(-?\d+)\s+\+(-?\d+)\+(-?\d+)', line)
    if not m:
        continue
    wid, w, h, _rx, _ry, ax, ay = m.groups()
    w, h, ax, ay = map(int, (w, h, ax, ay))
    if w >= 1500 and ay > 600 and h >= 30:
        if best is None or w > best[0]:
            best = (w, wid, ax, ay, h)
print(best[1] if best else "")
PY
)
[ -n "$PANEL" ] || fail "no bottom Plasma panel window found"
PANEL_GEO=$(xwininfo -id "$PANEL" | awk '/Width:/{w=$2}/Height:/{h=$2}/Absolute upper-left X:/{x=$4}/Absolute upper-left Y:/{y=$4}END{print w"x"h"+"x"+"y}')
LOCK=$(gdbus call --session --dest org.freedesktop.ScreenSaver --object-path /org/freedesktop/ScreenSaver \
  --method org.freedesktop.ScreenSaver.GetActive 2>/dev/null | tr -d '(),' || true)
SCALE=$(kreadconfig6 --file kdeglobals --group KScreen --key ScaleFactor 2>/dev/null || true)
{
  printf 'date=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  printf 'display=%s\n' "$DISPLAY"
  printf 'session_bus=%s\n' "$(sed 's|unix:path=.*/|unix:path=<runtime>/|' <<<"$DBUS_SESSION_BUS_ADDRESS")"
  printf 'xdg_session_type=%s\n' "${XDG_SESSION_TYPE:-x11}"
  printf 'desktop=%s\n' "$XDG_CURRENT_DESKTOP"
  printf 'plasma=%s\n' "$(plasmashell --version 2>/dev/null | head -1)"
  printf 'kwin=%s\n' "$(kwin_x11 --version 2>/dev/null | head -1)"
  printf 'screen=%s\n' "$(xdpyinfo | awk '/dimensions:/{print $2}')"
  printf 'scale_factor=%s\n' "$SCALE"
  printf 'panel_window=%s\n' "$PANEL"
  printf 'panel_geometry=%s\n' "$PANEL_GEO"
  printf 'lock_screen_active=%s\n' "$LOCK"
  printf 'look_and_feel=%s\n' "$(kreadconfig6 --file kdeglobals --group KDE --key LookAndFeelPackage 2>/dev/null)"
  printf 'icon_theme=%s\n' "$(kreadconfig6 --file kdeglobals --group Icons --key Theme 2>/dev/null)"
  printf 'window_bg=%s\n' "$(kreadconfig6 --file kdeglobals --group Colors:Window --key BackgroundNormal 2>/dev/null)"
} >"$EV/session.txt"
say "session: $DISPLAY panel=$PANEL lock=$LOCK scale=$SCALE"
gdbus call --session --dest org.kde.StatusNotifierWatcher --object-path /StatusNotifierWatcher \
  --method org.freedesktop.DBus.Properties.Get org.kde.StatusNotifierWatcher RegisteredStatusNotifierItems \
  2>/dev/null | redact >"$EV/bus-before.txt" || true

dump() { python3 "$HERE/menu-dump2.py" "$BUS" "$1" "$2"; }
id_of() { awk -F'|' -v l="$1" '$6==l {print $1; exit}' <<<"$2"; }
cov() { local c; c=$(id_of "Coverage" "$(dump 0 -1)" || true); if [ -n "$c" ]; then dump "$c" -1; fi; return 0; }
wait_cov() {
  local prefix=$1 tries=${2:-200} c
  for _ in $(seq 1 "$tries"); do
    c=$(cov)
    grep -qF -- "$prefix" <<<"$c" && return 0
    sleep 0.1
  done
  fail "coverage prefix '$prefix' never appeared (now '$(cov | head -1)')"
}
wait_bus() {
  local tray=$1
  BUS=""
  for _ in $(seq 1 150); do
    local list
    list=$(gdbus call --session --dest org.kde.StatusNotifierWatcher --object-path /StatusNotifierWatcher \
      --method org.freedesktop.DBus.Properties.Get org.kde.StatusNotifierWatcher RegisteredStatusNotifierItems 2>/dev/null || true)
    BUS=$(echo "$list" | grep -o "org.freedesktop.StatusNotifierItem-$tray-[0-9]*" | head -1 || true)
    [ -n "$BUS" ] && return 0
    sleep 0.1
  done
  fail "tray item for pid $tray was not registered"
}

icon_ref() {
  gdbus call --session --dest "$BUS" --object-path /StatusNotifierItem \
    --method org.freedesktop.DBus.Properties.Get org.kde.StatusNotifierItem IconPixmap \
    | python3 "$HERE/icon-dump.py" "$1" >"$2"
}

capture() {
  local group=$1 tag=$2 state=$3 covfile=$4
  local attempt ok=0
  for attempt in 1 2 3; do
    import -window "$PANEL" "$W/panel-$group-$tag.png" || fail "panel capture failed"
    icon_ref "$EV/$group/icon-$tag.png" "$EV/$group/icon-$tag.txt" || fail "IconPixmap read failed"
    python3 "$EV/locate_crop.py" "$W/panel-$group-$tag.png" "$EV/$group/icon-$tag.png" \
      "$EV/$group/crop-$tag.png" "$EV/$group/locate-$tag.json" >/dev/null
    python3 "$EV/measure.py" "$EV/$group/crop-$tag.png" "$EV/$group/icon-$tag.png" \
      "$state" "$EV/$group/contrast-$tag.json" | tee -a "$EV/run.log" >/dev/null
    if python3 -c 'import json,sys; sys.exit(0 if json.load(open(sys.argv[1]))["pass"] else 1)' \
        "$EV/$group/contrast-$tag.json"; then
      ok=1
      break
    fi
    say "retry capture $group/$tag (expected colours/pass not observed)"
    sleep 1
  done
  [ "$ok" = 1 ] || fail "panel contrast capture failed for $group/$tag"
  cov >"$covfile"
  rm -f "$W/panel-$group-$tag.png"
  say "captured $group/$tag state=$state"
}

# --- real run: no helper installed -----------------------------------------
BASE=$W/real
DATA=$BASE/data
XDG=$BASE/xdg
mkdir -p "$DATA" "$XDG/config" "$XDG/state" "$XDG/home" "$XDG/cache"
chmod 700 "$BASE" "$DATA" "$XDG"/*
export XDG_CONFIG_HOME=$XDG/config XDG_STATE_HOME=$XDG/state HOME=$XDG/home
export XDG_CACHE_HOME=$XDG/cache

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
say "real run scratch ready http=$HTTP socks=$SOCKS"

"$BIN" --data-dir "$DATA" run >"$W/real-run.out" 2>&1 &
RUNPID=$!
TRAY=""
for _ in $(seq 1 200); do
  TRAY=$(grep -ho 'tray_started pid=[0-9]*' "$DATA"/log/*.log 2>/dev/null | tail -1 | cut -d= -f2 || true)
  [ -n "$TRAY" ] && break
  sleep 0.1
done
[ -n "$TRAY" ] || fail "real supervisor did not start a tray"
wait_bus "$TRAY"
say "real tray pid=$TRAY bus=$BUS"
wait_cov "Coverage: off"
sleep 0.6
capture real off-helper-missing off "$EV/real/coverage-off-helper-missing.txt"
"$BIN" --data-dir "$DATA" status --json >"$W/real-status.json" 2>&1 || true
python3 - "$W/real-status.json" <<'PY' | redact >"$EV/real/status-summary.txt"
import json, sys
try:
    d = json.load(open(sys.argv[1]))["data"]
    tun = d.get("tun") or {}
    print("mode=%s ready=%s helper=%s state=%s desired=%s" % (
        d.get("mode"), d.get("ready"), tun.get("helper"), tun.get("state"), tun.get("desired")))
except Exception as e:
    print("status unavailable:", e)
PY

set +e
"$BIN" --data-dir "$DATA" tun on >"$W/tun-on.out" 2>"$W/tun-on.err"
RC=$?
set -e
{ echo "exit=$RC"; cat "$W/tun-on.out" "$W/tun-on.err"; } | redact >"$EV/real/tun-on.txt"
say "real tun on exit=$RC"
wait_cov "Coverage: unavailable"
sleep 0.8
capture real error-desired-missing error "$EV/real/coverage-error-desired-missing.txt"

kill -TERM "$RUNPID" 2>/dev/null || true
wait "$RUNPID" 2>/dev/null || true
RUNPID=""
pkill -9 -f -- "$BIN" 2>/dev/null || true
sleep 1
export HOME=$ORIG_HOME
if [ -n "$ORIG_XDG_CONFIG" ]; then export XDG_CONFIG_HOME=$ORIG_XDG_CONFIG; else unset XDG_CONFIG_HOME; fi
if [ -n "$ORIG_XDG_STATE" ]; then export XDG_STATE_HOME=$ORIG_XDG_STATE; else unset XDG_STATE_HOME; fi
if [ -n "$ORIG_XDG_CACHE" ]; then export XDG_CACHE_HOME=$ORIG_XDG_CACHE; else unset XDG_CACHE_HOME; fi
say "real run cleaned up"

# --- fake status: visual-only states ---------------------------------------
BASE=$W/fake
DATA=$BASE/data
XDG=$BASE/xdg
mkdir -p "$DATA" "$XDG/config" "$XDG/state" "$XDG/home" "$XDG/cache"
chmod 700 "$BASE" "$DATA" "$XDG"/*
export XDG_CONFIG_HOME=$XDG/config XDG_STATE_HOME=$XDG/state HOME=$XDG/home
export XDG_CACHE_HOME=$XDG/cache

python3 "$HERE/apply.py" "$HERE/states.json" "$BASE" off
python3 "$HERE/fake-tray.py" "$BASE" "$BIN" >"$W/fake-launcher.out" 2>&1 &
LAUNCHER=$!
for _ in $(seq 1 150); do [ -s "$BASE/tray.pid" ] && break; sleep 0.1; done
TRAY=$(cat "$BASE/tray.pid" 2>/dev/null || true)
[ -n "$TRAY" ] || fail "fake launcher did not report a tray pid"
wait_bus "$TRAY"
for _ in $(seq 1 100); do grep -q ' ready' "$BASE/control.log" 2>/dev/null && break; sleep 0.1; done
say "fake tray pid=$TRAY bus=$BUS"
wait_cov "Coverage: off"
sleep 0.6
capture fake off off "$EV/fake/coverage-off.txt"

for spec in "error|Coverage: error · desired on" "on|Coverage: on · desired on" \
            "starting|Coverage: starting · desired on" "conflict|Coverage: on · desired off"; do
  state=${spec%%|*}
  prefix=${spec#*|}
  python3 "$HERE/apply.py" "$HERE/states.json" "$BASE" "$state"
  wait_cov "$prefix"
  sleep 0.8
  capture fake "$state" "$state" "$EV/fake/coverage-$state.txt"
done

kill "$LAUNCHER" 2>/dev/null || true
wait "$LAUNCHER" 2>/dev/null || true
LAUNCHER=""
pkill -9 -f -- "$BIN" 2>/dev/null || true
sleep 1
say "fake run cleaned up"

# --- summary and montage ---------------------------------------------------
python3 "$EV/summarize.py" "$EV"
montage -background '#202326' -fill '#fcfcfc' -pointsize 12 \
  -label 'real off/missing' "$EV/real/crop-off-helper-missing.png" \
  -label 'real error/desired' "$EV/real/crop-error-desired-missing.png" \
  -label 'fake off' "$EV/fake/crop-off.png" \
  -label 'fake error' "$EV/fake/crop-error.png" \
  -label 'fake on' "$EV/fake/crop-on.png" \
  -label 'fake starting' "$EV/fake/crop-starting.png" \
  -label 'fake conflict' "$EV/fake/crop-conflict.png" \
  -tile 7x1 -geometry +6+6 "$EV/panel-crops-1x.png"
for f in real/crop-off-helper-missing real/crop-error-desired-missing \
         fake/crop-off fake/crop-error fake/crop-on fake/crop-starting fake/crop-conflict; do
  convert "$EV/$f.png" -filter point -resize 400% "$W/$(basename "$f")-4x.png"
done
montage -background '#202326' -fill '#fcfcfc' -pointsize 12 \
  -label 'real off/missing' "$W/crop-off-helper-missing-4x.png" \
  -label 'real error/desired' "$W/crop-error-desired-missing-4x.png" \
  -label 'fake off' "$W/crop-off-4x.png" \
  -label 'fake error' "$W/crop-error-4x.png" \
  -label 'fake on' "$W/crop-on-4x.png" \
  -label 'fake starting' "$W/crop-starting-4x.png" \
  -label 'fake conflict' "$W/crop-conflict-4x.png" \
  -tile 7x1 -geometry +6+6 "$EV/panel-crops-4x.png"
say "summary and montages written"
