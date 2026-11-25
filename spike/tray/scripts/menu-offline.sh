#!/bin/sh
# Offline evidence for the R15-A menu spike on an isolated session bus.
#
# Usage: scripts/menu-offline.sh [build-dir] [output-dir]
#
# The fake watcher from R13-A stands in for the desktop, and gdbus/busctl play
# the host: they call com.canonical.dbusmenu GetLayout/AboutToShow/Event. The
# output is raw D-Bus text plus the spike process log, so every verdict in
# MENU-FINDINGS.md has a locator. No desktop session or setting is touched.
set -u

BUILD=${1:-/tmp/opencode/menu-build}
OUTDIR=${2:-/tmp/opencode/menu-evidence}
mkdir -p "$OUTDIR"
OUT="$OUTDIR/menu-offline.txt"
SCRIPTS=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
: >"$OUT"

# Feed the orchestration to a file instead of `sh -s`: a script read from
# stdin can lose the rest of its text to a child command that reads stdin.
INNER="$OUTDIR/menu-offline-inner.sh"
cat >"$INNER" <<'EOF'
set -u
BUILD=$1
OUT=$2
SCRIPTS=$3

log() { printf '%s\n' "$*" >>"$OUT"; }
section() { printf '\n== %s\n' "$*" >>"$OUT"; }
wait_for() {
	pattern=$1
	i=0
	while [ "$i" -lt 400 ]; do
		grep -q "$pattern" "$OUT.spike.log" 2>/dev/null && return 0
		i=$((i + 1))
		sleep 0.01
	done
	return 1
}

"$BUILD/fake-watcher-amd64" >"$OUT.fake-watcher.log" 2>&1 &
FW=$!
i=0
while [ "$i" -lt 200 ]; do
	grep -q 'event=fake_watcher_ready' "$OUT.fake-watcher.log" 2>/dev/null && break
	i=$((i + 1))
	sleep 0.01
done
: >"$OUT.spike.log"
dbus-monitor --session "interface='com.canonical.dbusmenu'" >"$OUT.monitor.log" 2>&1 &
MON=$!
"$BUILD/menu-spike-amd64" -seconds 40 >"$OUT.spike.log" 2>&1 &
APP=$!
cleanup() {
	kill "$APP" "$MON" "$FW" 2>/dev/null
	wait "$APP" 2>/dev/null
	wait "$MON" 2>/dev/null
	wait "$FW" 2>/dev/null
}
trap cleanup EXIT INT TERM

if ! wait_for 'event=control_ready' || ! wait_for 'event=tray_registered'; then
	log "FAIL: menu-spike did not become ready"
	cat "$OUT.spike.log"
	exit 1
fi
ITEM=$(sed -n 's/.*item=\([^ ]*\).*/\1/p' "$OUT.spike.log" | head -1)
BUS=${ITEM%%/*}
CBUS=$(sed -n 's/.*event=control_ready bus=\([^ ]*\).*/\1/p' "$OUT.spike.log")
if [ -z "${ITEM:-}" ] || [ -z "${CBUS:-}" ]; then
	log "FAIL: could not parse item or control bus"
	exit 1
fi

menu() {
	method=$1
	shift
	gdbus call --session --dest "$BUS" --object-path /StatusNotifierMenu \
		--method "com.canonical.dbusmenu.$method" -- "$@"
}
ctl() {
	gdbus call --session --dest "$CBUS" --object-path /Control \
		--method dev.routelatch.MenuSpike.Apply "$1"
}
layout() { menu GetLayout "$1" "$2" '[]'; }
idof() { python3 "$SCRIPTS/dbusmenu-find-id.py" id "$1"; }
countprefix() { python3 "$SCRIPTS/dbusmenu-find-id.py" count-prefix "$1"; }
click() {
	out=$(busctl --user call "$BUS" /StatusNotifierMenu com.canonical.dbusmenu Event isvu "$1" "$2" v i 0 0 2>&1)
	rc=$?
	printf 'rc=%d output=%s' "$rc" "$out"
}
timed_ctl() {
	start=$(date +%s%N)
	result=$(ctl "$1")
	end=$(date +%s%N)
	log "timed_ctl stage=$1 result=$result elapsed_ms=$(( (end - start) / 1000000 ))"
}

log "item=$ITEM"
log "control_bus=$CBUS"
log "spike_binary=$BUILD/menu-spike-amd64"
log "spike_log=$OUT.spike.log"
log "dbus_monitor=$OUT.monitor.log"

section "initial full layout (depth 3: root, submenus, their children)"
INITIAL="$OUT.layout-initial.txt"
layout 0 3 >"$INITIAL"
log "bytes=$(wc -c <"$INITIAL") lines=$(wc -l <"$INITIAL")"
log "total_items_parsed=$(python3 "$SCRIPTS/dbusmenu-find-id.py" count-all <"$INITIAL")"
log "separators=$(grep -o "'type': <'separator'>" "$INITIAL" | wc -l)"
log "radio_items=$(grep -o "'toggle-type': <'radio'>" "$INITIAL" | wc -l)"
log "checkmark_items=$(grep -o "'toggle-type': <'checkmark'>" "$INITIAL" | wc -l)"
log "visible_false_items=$(grep -o "'visible': <false>" "$INITIAL" | wc -l)"
log "healthy_page1_rows=$(countprefix 'healthy-' <"$INITIAL")"
log "long_unicode_label_present=$(grep -c 'deliberately long mixed label' "$INITIAL")"
for label in 'Mode' 'Pool' 'Healthy (120)' 'Page 1/6 · 120 nodes' 'Stop listening…' 'Restart engine to resume…'; do
	if idof "$label" <"$INITIAL" >/dev/null; then
		log "has_label=$label yes"
	else
		log "has_label=$label no"
	fi
done

MODEPID=$(idof 'Mode' <"$INITIAL")
POOLID=$(idof 'Pool' <"$INITIAL")
HEALTHYID=$(idof 'Healthy (120)' <"$INITIAL")
ADVID=$(idof 'Advanced' <"$INITIAL")
EXPID=$(idof 'Experiment' <"$INITIAL")
log "ids mode=$MODEPID pool=$POOLID healthy=$HEALTHYID advanced=$ADVID experiment=$EXPID"

section "AboutToShow dirty-flag protocol"
log "AboutToShow(0) after initial GetLayout: $(menu AboutToShow 0)"
log "GetLayout(0,1) refresh"
layout 0 1 >/dev/null
log "AboutToShow(0) after refresh: $(menu AboutToShow 0)"
timed_ctl relabel
log "AboutToShow(0) after relabel: $(menu AboutToShow 0)"
layout 0 1 >"$OUT.layout-after-relabel.txt"
log "relabelled_status_present=$(grep -c 'relabelled' "$OUT.layout-after-relabel.txt")"
log "AboutToShow(0) after GetLayout: $(menu AboutToShow 0)"

section "runtime add -> LayoutUpdated; remove"
timed_ctl add
log "AboutToShow(0) after add: $(menu AboutToShow 0)"
layout 0 1 >"$OUT.layout-after-add.txt"
log "job_item_present=$(grep -c 'Job: pool refresh running' "$OUT.layout-after-add.txt")"
timed_ctl remove
layout 0 1 >"$OUT.layout-after-remove.txt"
log "job_item_after_remove=$(grep -c 'Job: pool refresh running' "$OUT.layout-after-remove.txt")"

section "visible/enabled toggles on existing items"
timed_ctl visibility
layout "$ADVID" 1 >"$OUT.layout-advanced-visible.txt"
log "advanced_children:"; cat "$OUT.layout-advanced-visible.txt" >>"$OUT"
timed_ctl visibility-restore
layout "$ADVID" 1 >"$OUT.layout-advanced-restored.txt"
log "advanced_children_restored:"; cat "$OUT.layout-advanced-restored.txt" >>"$OUT"

section "radio/checkmark state, programmatic and click-driven"
timed_ctl radio
layout "$MODEPID" 1 >"$OUT.layout-mode-radio.txt"
layout 0 2 >"$OUT.layout-root-radio.txt"
log "mode:"; cat "$OUT.layout-mode-radio.txt" >>"$OUT"
ALLLABEL='All direct'
ALLID=$(idof "$ALLLABEL" <"$OUT.layout-mode-radio.txt")
log "click All direct id=$ALLID: $(click "$ALLID" clicked)"
sleep 0.2
layout "$MODEPID" 1 >"$OUT.layout-mode-clicked.txt"
log "mode_after_click:"; cat "$OUT.layout-mode-clicked.txt" >>"$OUT"
timed_ctl radio-restore

section "pool page 1 -> 2 -> clamp 99 (176 fixture, 20 per page)"
layout "$HEALTHYID" 1 >"$OUT.layout-pool-page1.txt"
log "page1_items=$(python3 "$SCRIPTS/dbusmenu-find-id.py" count-all <"$OUT.layout-pool-page1.txt") page1_prefixed_rows=$(countprefix 'healthy-' <"$OUT.layout-pool-page1.txt") page1_long_row=$(grep -c 'deliberately long mixed label' "$OUT.layout-pool-page1.txt") page1_label=$(grep -o "Page 1/6 · 120 nodes" "$OUT.layout-pool-page1.txt" | head -1)"
timed_ctl pool-page:2
layout "$HEALTHYID" 1 >"$OUT.layout-pool-page2.txt"
log "page2_items=$(python3 "$SCRIPTS/dbusmenu-find-id.py" count-all <"$OUT.layout-pool-page2.txt") page2_prefixed_rows=$(countprefix 'healthy-' <"$OUT.layout-pool-page2.txt") page2_label=$(grep -o "Page 2/6 · 120 nodes" "$OUT.layout-pool-page2.txt" | head -1)"
NEXTID=$(idof 'Next page' <"$OUT.layout-pool-page2.txt")
log "click pool Next page id=$NEXTID: $(click "$NEXTID" clicked)"
sleep 0.2
layout "$HEALTHYID" 1 >"$OUT.layout-pool-click-next.txt"
log "click_next_rows=$(countprefix 'healthy-' <"$OUT.layout-pool-click-next.txt") click_next_label=$(grep -o "Page 3/6 · 120 nodes" "$OUT.layout-pool-click-next.txt" | head -1)"
timed_ctl pool-page:99
layout "$HEALTHYID" 1 >"$OUT.layout-pool-page99.txt"
log "page99_rows=$(countprefix 'healthy-' <"$OUT.layout-pool-page99.txt") page99_label=$(grep -o "Page 6/6 · 120 nodes" "$OUT.layout-pool-page99.txt" | head -1)"
log "pool_page_long_label_pages=$(grep -l 'deliberately long mixed label' "$OUT".layout-pool-page*.txt | wc -l)"

section "two-step confirmation items inserted before the status line"
timed_ctl confirm
layout 0 1 >"$OUT.layout-confirm.txt"
log "first_children_prefix:"; cut -c1-400 "$OUT.layout-confirm.txt" >>"$OUT"
log "confirm_present=$(grep -c 'Confirm: Stop listening' "$OUT.layout-confirm.txt") cancel_present=$(grep -c "'label': <'Cancel'>" "$OUT.layout-confirm.txt")"
CANCELID=$(idof 'Cancel' <"$OUT.layout-confirm.txt")
log "click Cancel id=$CANCELID: $(click "$CANCELID" clicked)"
sleep 0.2
timed_ctl unconfirm

section "empty submenu conversion (existing item removed, then refilled)"
operation=$(ctl empty-submenu)
log "empty-submenu: $operation"
layout "$EXPID" 1 >"$OUT.layout-experiment-empty.txt"
log "experiment_empty:"; cat "$OUT.layout-experiment-empty.txt" >>"$OUT"
operation=$(ctl refill-submenu)
log "refill-submenu: $operation"
layout "$EXPID" 1 >"$OUT.layout-experiment-refilled.txt"
log "experiment_refilled:"; cat "$OUT.layout-experiment-refilled.txt" >>"$OUT"

section "host-like events: opened / closed / clicked"
log "Event opened: $(click 0 opened)"
log "Event closed: $(click 0 closed)"
REFRESHID=$(idof 'Refresh status' <"$INITIAL")
log "click Refresh status id=$REFRESHID: $(click "$REFRESHID" clicked)"
sleep 0.2
log "handler_lines:"
grep 'event=menu_click' "$OUT.spike.log" | tail -8 >>"$OUT"

section "multilingual ESC-free label"
timed_ctl raw-label
layout 0 1 >"$OUT.layout-raw-label.txt"
log "raw_label_present=$(grep -c 'δοκιμή · آزمون · テスト' "$OUT.layout-raw-label.txt")"
log "esc_byte_in_label=$(python3 -c 'import sys; print(open(sys.argv[1], encoding="utf-8").read().count(chr(27)))' "$OUT.layout-raw-label.txt")"

section "dbusmonitor summary (isolated bus, so the only calls are this script's)"
log "signals LayoutUpdated=$(grep -c 'member=LayoutUpdated' "$OUT.monitor.log")"
log "signals ItemsPropertiesUpdated=$(grep -c 'member=ItemsPropertiesUpdated' "$OUT.monitor.log")"
log "method GetLayout=$(grep -c 'member=GetLayout' "$OUT.monitor.log")"
log "method AboutToShow=$(grep -c 'member=AboutToShow' "$OUT.monitor.log")"
log "method Event=$(grep -c 'member=Event' "$OUT.monitor.log")"
TIMES=$(grep -E 'member=(LayoutUpdated|ItemsPropertiesUpdated)' "$OUT.monitor.log" | sed -n 's/^signal time=\([0-9.]*\).*/\1/p')
log "first_signal_time=$(printf '%s\n' "$TIMES" | head -1) last_signal_time=$(printf '%s\n' "$TIMES" | tail -1)"

section "cleanup"
kill "$APP" 2>/dev/null
wait "$APP" 2>/dev/null
log "spike_exited"
EOF

dbus-run-session -- sh "$INNER" "$BUILD" "$OUT" "$SCRIPTS"

printf 'wrote %s\n' "$OUT"
