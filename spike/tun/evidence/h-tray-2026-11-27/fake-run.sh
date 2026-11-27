#!/usr/bin/env bash
# R16-H tray acceptance, part 2: the real product tray child against a fake
# management socket. Catches every tray state that needs no host TUN; the
# visual states are explicitly labelled fake-status in H-TRAY.md.
set -euo pipefail

REPO=${REPO:-<repo>}  # set REPO to the repository root
WORK=${WORK:-${TMPDIR:-/tmp}/routelatch-h-tray}  # scratch root
BIN=${BIN:-$WORK/bin/routelatch}
TS=$(date -u +%Y%m%dT%H%M%SZ)
BASE=${BASE:-$WORK/fake-$TS}
EV=${EV:-$REPO/spike/tun/evidence/h-tray-2026-11-27/fake}
HERE=${HERE:-$REPO/spike/tun/evidence/h-tray-2026-11-27}
DATA=$BASE/data
XDG=$BASE/xdg
mkdir -p "$DATA" "$XDG/config" "$XDG/state" "$XDG/home" "$XDG/cache" "$EV"
chmod 700 "$BASE" "$DATA" "$XDG"/*

export DISPLAY=${DISPLAY:-:0}
export DBUS_SESSION_BUS_ADDRESS=${DBUS_SESSION_BUS_ADDRESS:-unix:path=${XDG_RUNTIME_DIR:-/run/user/$(id -u)}/bus}
export XDG_CONFIG_HOME=$XDG/config XDG_STATE_HOME=$XDG/state HOME=$XDG/home
export XDG_CACHE_HOME=$XDG/cache XDG_CURRENT_DESKTOP=KDE

say() { printf '%s %s\n' "$(date -u +%H:%M:%S)" "$*" | tee -a "$EV/steps.txt"; }
fail() { say "FAIL: $*"; exit 1; }
dump() { python3 "$HERE/menu-dump2.py" "$BUS" "$1" "$2"; }
root() { dump 0 -1; }
id_of() { awk -F'|' -v l="$1" '$6==l {print $1; exit}' <<<"$2"; }
has_label() { awk -F'|' -v l="$1" '$6==l {found=1} END{exit !found}' <<<"$2"; }
has_prefix() { awk -F'|' -v p="$1" 'index($6,p)==1 {found=1} END{exit !found}' <<<"$2"; }
id_prefix() { awk -F'|' -v p="$1" 'index($6,p)==1 {print $1; exit}' <<<"$2"; }
click() {
  local id=$1
  [ -n "$id" ] || fail "click with empty id"
  say "click id=$id"
  gdbus call --session --dest "$BUS" --object-path /StatusNotifierMenu \
    --method com.canonical.dbusmenu.Event "$id" clicked "<''>" 0 >/dev/null
}
header() { root | awk -F'|' 'NR>1 && $6!="" {print $6; exit}'; }
state() {
  say "state=$1"
  python3 "$HERE/apply.py" "$HERE/states.json" "$BASE" "$1"
}
wait_label() {
  local label=$1 tries=${2:-120}
  for _ in $(seq 1 "$tries"); do
    has_label "$label" "$(root)" && return 0
    sleep 0.1
  done
  fail "label '$label' never appeared"
}
wait_prefix() {
  local prefix=$1 tries=${2:-120}
  for _ in $(seq 1 "$tries"); do
    has_prefix "$prefix" "$(root)" && return 0
    sleep 0.1
  done
  fail "label prefix '$prefix' never appeared"
}
wait_no_prefix() {
  local prefix=$1 tries=${2:-120}
  for _ in $(seq 1 "$tries"); do
    has_prefix "$prefix" "$(root)" || return 0
    sleep 0.1
  done
  fail "label prefix '$prefix' stayed"
}
cov_id() { id_of "Coverage" "$(root)"; }
cov_dump() { local c; c=$(cov_id); [ -n "$c" ] || fail "no Coverage"; dump "$c" -1; }
icon() {
  gdbus call --session --dest "$BUS" --object-path /StatusNotifierItem \
    --method org.freedesktop.DBus.Properties.Get org.kde.StatusNotifierItem IconPixmap \
    | python3 "$HERE/icon-dump.py" "$EV/icon-$1.png" | tee -a "$EV/icons.txt"
}
tooltip() {
  gdbus call --session --dest "$BUS" --object-path /StatusNotifierItem \
    --method org.freedesktop.DBus.Properties.Get org.kde.StatusNotifierItem ToolTip \
    | tee -a "$EV/tooltips.txt"
}
count_op() { grep -c "\"op\": \"$1\"" "$BASE/requests.log" 2>/dev/null || true; }
wait_count() {
  local op=$1 want=$2 tries=${3:-120}
  for _ in $(seq 1 "$tries"); do
    [ "$(count_op "$op")" = "$want" ] && return 0
    sleep 0.1
  done
  fail "$op count is $(count_op "$op"), want $want"
}

cleanup() {
  kill "${NMON:-}" 2>/dev/null || true
  kill "${LAUNCHER:-}" 2>/dev/null || true
  wait "${LAUNCHER:-}" 2>/dev/null || true
  for pid in $(pgrep -f -- "$BIN __app-tray" || true); do
    [ "$pid" = "${TRAY:-}" ] && kill -9 "$pid" 2>/dev/null || true
  done
}
trap cleanup EXIT INT TERM

dbus-monitor "interface='org.freedesktop.Notifications'" >"$EV/notifications.log" 2>&1 &
NMON=$!
sleep 0.5

# Fund the first fixture before the tray starts.
python3 "$HERE/apply.py" "$HERE/states.json" "$BASE" off
python3 "$HERE/fake-tray.py" "$BASE" "$BIN" >"$BASE/launcher.out" 2>&1 &
LAUNCHER=$!
for _ in $(seq 1 100); do
  [ -s "$BASE/tray.pid" ] && break
  sleep 0.05
done
TRAY=$(cat "$BASE/tray.pid")
[ -n "$TRAY" ] || fail "launcher did not report a tray pid"
BUS=""
for _ in $(seq 1 120); do
  LIST=$(gdbus call --session --dest org.kde.StatusNotifierWatcher --object-path /StatusNotifierWatcher \
    --method org.freedesktop.DBus.Properties.Get org.kde.StatusNotifierWatcher RegisteredStatusNotifierItems)
  BUS=$(echo "$LIST" | grep -o "org.freedesktop.StatusNotifierItem-$TRAY-[0-9]*" | head -1 || true)
  [ -n "$BUS" ] && break
  sleep 0.1
done
[ -n "$BUS" ] || fail "fake tray item for pid $TRAY not registered"
for _ in $(seq 1 100); do
  grep -q " ready" "$BASE/control.log" 2>/dev/null && break
  sleep 0.05
done
printf 'base=%s\ntray=%s\nbus=%s\nfake_socket=%s\nbinary=%s\nhead=%s\n' \
  "$BASE" "$TRAY" "$BUS" "$BASE/run/control.sock" "$BIN" "$(git -C "$REPO" rev-parse HEAD)" >"$EV/run-env.txt"
say "fake tray ready pid=$TRAY bus=$BUS"

wait_prefix "Coverage: off · desired off"

# --- off / desired-off: icon, summary, disabled disable reason ---
state off
wait_prefix "Coverage: off · desired off"
cov_dump >"$EV/coverage-off.txt"
icon off
tooltip
root >"$EV/layout-off.txt"

# --- pending dialog + cancel, no request ---
COV=$(cov_id)
EN=$(id_of "Enable system-wide coverage…" "$(dump "$COV" -1)")
[ -n "$EN" ] || fail "Enable missing"
click "$EN"
wait_prefix "Confirm: Enable system-wide coverage"
printf '%s\n' "$(root)" >"$EV/layout-enable-pending.txt"
has_label "Cancel" "$(root)" || fail "no Cancel for enable pending"
CANCEL=$(id_of "Cancel" "$(root)")
click "$CANCEL"
wait_no_prefix "Confirm:"
[ "$(count_op tun.enable)" = "0" ] || fail "cancel sent a tun.enable request"
say "enable pending + cancel: zero requests"

# --- replacement: enable pending replaced by disable pending ---
state error
wait_prefix "Coverage: error · desired on"
COV=$(cov_id)
EN=$(id_of "Enable system-wide coverage…" "$(dump "$COV" -1)")
DIS=$(id_of "Disable system-wide coverage…" "$(dump "$COV" -1)")
[ -n "$EN" ] && [ -n "$DIS" ] || fail "error state needs both actions enabled"
click "$EN"
wait_prefix "Confirm: Enable system-wide coverage"
click "$DIS"
wait_prefix "Confirm: Disable system-wide coverage"
P=$(root)
has_prefix "Confirm: Enable" "$P" && fail "old pending was not replaced"
printf '%s\n' "$P" >"$EV/layout-pending-replaced.txt"
say "pending replaced by the newer action"
click "$(id_of "Cancel" "$P")"
wait_no_prefix "Confirm:"

# --- stale: 60s expiry without a request ---
COV=$(cov_id)
EN=$(id_of "Enable system-wide coverage…" "$(dump "$COV" -1)")
click "$EN"
wait_prefix "Confirm: Enable system-wide coverage"
say "waiting 62s for the pending to expire"
sleep 62
if has_prefix "Confirm:" "$(root)"; then fail "pending did not expire after 60s"; fi
[ "$(count_op tun.enable)" = "0" ] || fail "expired pending sent a request"
printf '%s\n' "$(root)" >"$EV/layout-after-stale.txt"
say "stale pending expired, zero requests"

# --- two-step enable: confirm runs exactly one request ---
state error
wait_prefix "Coverage: error · desired on"
COV=$(cov_id)
EN=$(id_of "Enable system-wide coverage…" "$(dump "$COV" -1)")
click "$EN"
wait_prefix "Confirm: Enable system-wide coverage"
CONFIRM=$(id_prefix "Confirm: Enable system-wide coverage" "$(root)")
click "$CONFIRM"
wait_count tun.enable 1
# A replay of the old confirm id must not send a second request.
click "$CONFIRM" || true
sleep 1
[ "$(count_op tun.enable)" = "1" ] || fail "duplicate tun.enable request"
state on
wait_prefix "Coverage: on · desired on"
cov_dump >"$EV/coverage-on.txt"
icon on
tooltip
root >"$EV/layout-on.txt"
say "enable confirmed once, coverage on"

# --- warnings submenu with every documented warning id ---
WARN=$(id_of "Warnings (4)" "$(cov_dump)")
[ -n "$WARN" ] || fail "Warnings (4) submenu missing"
dump "$WARN" -1 >"$EV/warnings.txt"
say "warnings captured"

# --- two-step disable (coverage reduction) ---
COV=$(cov_id)
DIS=$(id_of "Disable system-wide coverage…" "$(dump "$COV" -1)")
click "$DIS"
wait_prefix "Confirm: Disable system-wide coverage"
printf '%s\n' "$(root)" >"$EV/layout-disable-pending.txt"
CONFIRM=$(id_of "Confirm: Disable system-wide coverage" "$(root)")
click "$CONFIRM"
wait_count tun.disable 1
state off
wait_prefix "Coverage: off · desired off"
[ "$(count_op tun.disable)" = "1" ] || fail "duplicate or missing tun.disable"
icon off-after-disable
cov_dump >"$EV/coverage-off-after-disable.txt"
say "disable confirmed once, coverage off"

# --- conflict: on without request ---
state conflict
wait_prefix "Coverage: on · desired off · active without request"
cov_dump >"$EV/coverage-conflict.txt"
icon conflict
tooltip
say "conflict state captured"

# --- incompatible helper ---
state incompatible
wait_prefix "Coverage: off · desired off"
cov_dump >"$EV/coverage-incompatible.txt"
say "incompatible helper captured"

# --- starting/pending ---
state starting
wait_prefix "Coverage: starting · desired on"
cov_dump >"$EV/coverage-starting.txt"
icon pending
tooltip
say "pending state captured"

# --- older payload: no tun object ---
state unknown
wait_prefix "Coverage: unknown · status not reported"
cov_dump >"$EV/coverage-unknown.txt"
icon unknown
tooltip
say "unknown payload captured"

# --- D21: URL-shaped interface/detail/reason/warning must not be rendered ---
state inject
wait_prefix "Coverage: error · desired on"
cov_dump >"$EV/coverage-inject.txt"
if grep -Eqi 'canary|https?://' "$EV/coverage-inject.txt"; then
  fail "URL-shaped status text reached a Coverage label"
fi
grep -q 'Error: coverage error' "$EV/coverage-inject.txt" || fail "unknown last_error did not use the fixed label"
grep -q 'Interface:' "$EV/coverage-inject.txt" && fail "invalid interface name was rendered"
grep -q 'Warnings (0)' "$EV/coverage-inject.txt" || fail "unknown warning id was not dropped"
say "injected URL-shaped status text not rendered in Coverage"

# --- notifications: no destination/URL, counts only ---
sleep 1
python3 "$HERE/notify-summary.py" "$EV/notifications.log" >"$EV/notifications-summary.txt"
if grep -Eqi 'canary|https?://' "$EV/notifications.log"; then
  fail "URL-shaped text in a notification"
fi
say "notification scan: no URL/destination"

printf 'tun.enable requests=%s\ntun.disable requests=%s\n' "$(count_op tun.enable)" "$(count_op tun.disable)" >"$EV/request-counts.txt"
awk -F'"op": "' '/"op": "/{split($2,a,"\""); c[a[1]]++} END{for (k in c) print k "=" c[k]}' \
  "$BASE/requests.log" | sort >"$EV/requests-summary.txt"
say "fake run complete"
