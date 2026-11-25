#!/bin/sh
# Live KDE evidence for the R15-A menu spike.
#
# Usage: scripts/kde-menu-evidence.sh <menu-spike-binary> [output-dir]
#
# Requires an existing KDE session bus (DBUS_SESSION_BUS_ADDRESS) and an X11
# DISPLAY. It creates exactly one temporary tray item (id
# routelatch-tray-menu-spike), drives com.canonical.dbusmenu directly, and, if
# the magenta icon can be found in a screenshot, opens the real Plasma menu
# once to observe AboutToShow/opened and live updates. It closes the menu,
# kills the process, and changes no desktop setting.
set -u

BIN=${1:?usage: scripts/kde-menu-evidence.sh <menu-spike-binary> [output-dir]}
OUTDIR=${2:-/tmp/opencode/menu-evidence}
BUSADDR=${DBUS_SESSION_BUS_ADDRESS:?set DBUS_SESSION_BUS_ADDRESS to the KDE session bus}
export DBUS_SESSION_BUS_ADDRESS="$BUSADDR"
export DISPLAY=${DISPLAY:-:0}
mkdir -p "$OUTDIR"
SCRIPTS=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
STAMP=$(date -u +%Y%m%dT%H%M%SZ)
BASE="$OUTDIR/menu-kde-$STAMP"
OUT="$BASE.txt"
MON="$BASE.monitor.log"
: >"$OUT"

log() { printf '%s\n' "$*" >>"$OUT"; }
section() { printf '\n== %s\n' "$*" >>"$OUT"; }
NOW() { date +%s.%N; }

wait_for() {
	pattern=$1
	i=0
	while [ "$i" -lt 600 ]; do
		grep -q "$pattern" "$BASE.spike.log" 2>/dev/null && return 0
		i=$((i + 1))
		sleep 0.01
	done
	return 1
}

dbus-monitor --session "interface='com.canonical.dbusmenu'" >"$MON" 2>&1 &
MONPID=$!
START=$(NOW)
"$BIN" -id routelatch-tray-menu-spike -seconds 90 -open-test >"$BASE.spike.log" 2>&1 &
APP=$!
cleanup() {
	kill "$APP" "$MONPID" 2>/dev/null
	wait "$APP" 2>/dev/null
	wait "$MONPID" 2>/dev/null
}
trap cleanup EXIT INT TERM

if ! wait_for 'event=control_ready' || ! wait_for 'event=tray_registered'; then
	log "FAIL: menu-spike did not register"
	cat "$BASE.spike.log" >>"$OUT"
	exit 1
fi
ITEM=$(sed -n 's/.*item=\([^ ]*\).*/\1/p' "$BASE.spike.log" | head -1)
BUS=${ITEM%%/*}
CBUS=$(sed -n 's/.*event=control_ready bus=\([^ ]*\).*/\1/p' "$BASE.spike.log")
if [ -z "${ITEM:-}" ] || [ -z "${CBUS:-}" ]; then
	log "FAIL: could not parse item or control bus"
	exit 1
fi
OWNER=$(gdbus call --session --dest org.freedesktop.DBus --object-path /org/freedesktop/DBus \
	--method org.freedesktop.DBus.GetNameOwner "$BUS" 2>/dev/null |
	sed -n "s/('\([^']*\)',)/\1/p")
PLASMA_PID=$(pgrep -x plasmashell | head -1)
HOSTNAME="org.kde.StatusNotifierHost-$PLASMA_PID"
HOSTOWNER=$(gdbus call --session --dest org.freedesktop.DBus --object-path /org/freedesktop/DBus \
	--method org.freedesktop.DBus.GetNameOwner "$HOSTNAME" 2>/dev/null |
	sed -n "s/('\([^']*\)',)/\1/p")

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
	printf 'rc=%d output=%s' "$?" "$out"
}
# open_menu_once takes a label, screenshots, finds the magenta icon, clicks it,
# and reports success only when plasmashell calls AboutToShow on this item.
# A failed attempt presses Escape in case another tray menu was opened.
open_menu_once() {
	label=$1
	shot="$BASE.open-attempt-$label.png"
	import -window root "$shot"
	found=$(python3 "$SCRIPTS/tray-open-menu.py" find "$shot" 2>&1) && rc=0 || rc=$?
	log "open_attempt=$label icon_find=$found rc=$rc"
	[ "$rc" = 0 ] || return 1
	ox=${found% *}
	oy=${found#* }
	started=$(NOW)
	python3 "$SCRIPTS/tray-open-menu.py" click "$ox" "$oy"
	sleep 1.2
	open_shot="$BASE.menu-open-$label.png"
	import -window root "$open_shot"
	log "open_attempt=$label click x=$ox y=$oy host_calls:"
	host_calls "$started" >>"$OUT"
	if host_calls "$started" | grep -q 'member=AboutToShow'; then
		OPEN_X=$ox
		OPEN_Y=$oy
		OPEN_S=$started
		OPEN_SHOT=$open_shot
		return 0
	fi
	python3 "$SCRIPTS/tray-open-menu.py" escape
	sleep 0.5
	return 1
}
host_calls() {
	[ -n "${HOSTOWNER:-}" ] || return 0
	python3 "$SCRIPTS/dbusmenu-monitor.py" host-calls "$MON" "$HOSTOWNER" "$BUS|$OWNER" "$1"
}
item_signals() {
	[ -n "${OWNER:-}" ] || return 0
	python3 "$SCRIPTS/dbusmenu-monitor.py" signals "$MON" "$OWNER" "$1"
}
host_latency() {
	[ -n "${HOSTOWNER:-}" ] || { printf 'none'; return 0; }
	python3 "$SCRIPTS/dbusmenu-monitor.py" latency "$MON" "$HOSTOWNER" "$BUS|$OWNER" "$1" "$2"
}
menu_events() {
	[ -n "${HOSTOWNER:-}" ] || return 0
	python3 "$SCRIPTS/dbusmenu-monitor.py" menu-events "$MON" "$HOSTOWNER" "$BUS|$OWNER" "$1"
}
host_call_count() {
	[ -n "${HOSTOWNER:-}" ] || { printf '0'; return 0; }
	python3 "$SCRIPTS/dbusmenu-monitor.py" count-calls "$MON" "$HOSTOWNER" "$BUS|$OWNER" "$1" "$2"
}
timed_ctl() {
	start=$(date +%s%N)
	result=$(ctl "$1")
	end=$(date +%s%N)
	log "timed_ctl stage=$1 result=$result elapsed_ms=$(( (end - start) / 1000000 ))"
}

log "date_utc=$STAMP"
log "menu_spike=$BIN"
log "item=$ITEM"
log "item_owner=$OWNER"
log "control_bus=$CBUS"
log "plasma_host_name=$HOSTNAME"
log "plasma_host_owner=$HOSTOWNER"
log "session=${XDG_SESSION_TYPE:-unset} desktop=${XDG_CURRENT_DESKTOP:-unset} display=${DISPLAY:-unset}"
log "plasmashell=$(plasmashell --version 2>/dev/null || true)"
log "kwin=$(kwin_x11 --version 2>/dev/null | head -1 || true)"
log "watcher_lists_item=$(gdbus call --session --dest org.kde.StatusNotifierWatcher --object-path /StatusNotifierWatcher \
	--method org.freedesktop.DBus.Properties.Get org.kde.StatusNotifierWatcher RegisteredStatusNotifierItems 2>/dev/null | grep -c "$BUS")"

section "host queries after registration (plasmashell only)"
sleep 1.5
host_calls "$START" >>"$OUT"
log "startup_GetLayout_latency_ms=$(host_latency "$START" GetLayout)"

section "initial layout (depth 3)"
INITIAL="$BASE.layout-initial.txt"
layout 0 3 >"$INITIAL"
log "bytes=$(wc -c <"$INITIAL") total_items=$(python3 "$SCRIPTS/dbusmenu-find-id.py" count-all <"$INITIAL")"
log "separators=$(grep -o "'type': <'separator'>" "$INITIAL" | wc -l)"
log "radio=$(grep -o "'toggle-type': <'radio'>" "$INITIAL" | wc -l) checkmark=$(grep -o "'toggle-type': <'checkmark'>" "$INITIAL" | wc -l)"
log "long_unicode_label=$(grep -c 'deliberately long mixed label' "$INITIAL") multilingual_control_free_label=$(grep -c 'δοκιμή · آزمون · テスト' "$INITIAL")"
MODEPID=$(idof 'Mode' <"$INITIAL")
POOLID=$(idof 'Pool' <"$INITIAL")
HEALTHYID=$(idof 'Healthy (120)' <"$INITIAL")
ADVID=$(idof 'Advanced' <"$INITIAL")
EXPID=$(idof 'Experiment' <"$INITIAL")
log "ids mode=$MODEPID pool=$POOLID healthy=$HEALTHYID advanced=$ADVID experiment=$EXPID"

section "react: relabel an existing item (property-only change)"
sleep 0.5
S=$(NOW)
timed_ctl relabel
sleep 1
log "host_calls:"; host_calls "$S" >>"$OUT"
log "item_signals:"; item_signals "$S" >>"$OUT"
log "host_GetLayout_latency_ms=$(host_latency "$S" GetLayout)"
layout 0 1 >"$BASE.layout-relabel.txt"
log "relabelled=$(grep -c 'relabelled' "$BASE.layout-relabel.txt")"

section "react: add a new root item (structural change)"
S=$(NOW)
timed_ctl add
sleep 1
log "host_calls:"; host_calls "$S" >>"$OUT"
log "item_signals:"; item_signals "$S" >>"$OUT"
log "host_GetLayout_latency_ms=$(host_latency "$S" GetLayout)"
layout 0 2 >"$BASE.layout-add.txt"
log "job_item=$(grep -c 'Job: pool refresh running' "$BASE.layout-add.txt")"

section "react: remove that item, toggle visible/enabled, change radio/checkmark"
S=$(NOW)
timed_ctl remove
sleep 0.5
log "remove_host_calls:"; host_calls "$S" >>"$OUT"
S=$(NOW)
timed_ctl visibility
sleep 0.5
log "visibility_host_calls:"; host_calls "$S" >>"$OUT"
log "visibility_item_signals:"; item_signals "$S" >>"$OUT"
layout "$ADVID" 1 >"$BASE.layout-visibility.txt"
log "advanced_children:"; cat "$BASE.layout-visibility.txt" >>"$OUT"
S=$(NOW)
timed_ctl radio
sleep 0.5
log "radio_host_calls:"; host_calls "$S" >>"$OUT"
log "radio_item_signals:"; item_signals "$S" >>"$OUT"
layout "$MODEPID" 1 >"$BASE.layout-radio.txt"
log "mode_after_radio:"; cat "$BASE.layout-radio.txt" >>"$OUT"
layout 0 2 >"$BASE.layout-radio-root.txt"
log "autostart_line=$(grep -o "'label': <'Start at login: [^']*'>" "$BASE.layout-radio-root.txt" | head -1)"
ALLID=$(idof 'All direct' <"$BASE.layout-radio.txt")
S=$(NOW)
log "click All direct id=$ALLID: $(click "$ALLID" clicked)"
sleep 0.6
log "click_host_calls:"; host_calls "$S" >>"$OUT"
layout "$MODEPID" 1 >"$BASE.layout-mode-clicked.txt"
log "mode_after_click:"; cat "$BASE.layout-mode-clicked.txt" >>"$OUT"

section "pool pagination: 176-node fixture, 20 per page, clamp"
S=$(NOW)
timed_ctl pool-page:2
sleep 0.7
log "page2_host_calls:"; host_calls "$S" >>"$OUT"
layout "$HEALTHYID" 1 >"$BASE.layout-pool-page2.txt"
log "page2_items=$(python3 "$SCRIPTS/dbusmenu-find-id.py" count-all <"$BASE.layout-pool-page2.txt") page2_label=$(grep -o 'Page 2/6 · 120 nodes' "$BASE.layout-pool-page2.txt" | head -1)"
S=$(NOW)
timed_ctl pool-page:99
sleep 0.7
log "clamp_host_calls:"; host_calls "$S" >>"$OUT"
layout "$HEALTHYID" 1 >"$BASE.layout-pool-page99.txt"
log "page99_items=$(python3 "$SCRIPTS/dbusmenu-find-id.py" count-all <"$BASE.layout-pool-page99.txt") page99_label=$(grep -o 'Page 6/6 · 120 nodes' "$BASE.layout-pool-page99.txt" | head -1)"
log "page99_has_long_label=$(grep -c 'deliberately long mixed label' "$BASE.layout-pool-page99.txt")"

section "multilingual label (live)"
S=$(NOW)
timed_ctl raw-label
sleep 0.5
log "raw_label_host_calls:"; host_calls "$S" >>"$OUT"
log "raw_label_item_signals:"; item_signals "$S" >>"$OUT"
layout 0 1 >"$BASE.layout-raw-label.txt"
log "raw_label_present=$(grep -c 'δοκιμή · آزمون · テスト' "$BASE.layout-raw-label.txt")"

section "AboutToShow protocol and GetLayout latency (menu closed)"
layout 0 1 >/dev/null
log "AboutToShow(0)_after_fresh_layout=$(menu AboutToShow 0)"
S=$(NOW)
timed_ctl relabel
log "AboutToShow(0)_after_relabel=$(menu AboutToShow 0)"
LAT_START=$(NOW)
layout 0 1 >/dev/null
LAT_END=$(NOW)
log "closed_GetLayout_roundtrip_ms=$(awk -v a="$LAT_START" -v b="$LAT_END" 'BEGIN { printf "%.1f", (b - a) * 1000 }')"

section "real open test"
OPEN=skipped
MENU_X=0
MENU_Y=0
MENU_W=0
MENU_H=0
if command -v import >/dev/null 2>&1 && python3 -c 'import PIL, Xlib' >/dev/null 2>&1; then
	OPENED=1
	ATTEMPT=0
	while [ "$ATTEMPT" -lt 3 ]; do
		ATTEMPT=$((ATTEMPT + 1))
		if open_menu_once "$ATTEMPT"; then
			OPENED=0
			break
		fi
		sleep 0.4
	done
	if [ "$OPENED" = 0 ]; then
		OPEN=yes
		log "open_verified_by_host=AboutToShow_on_our_bus attempt=$ATTEMPT"
		SCREEN_W=$(python3 -c 'from PIL import Image; import sys; print(Image.open(sys.argv[1]).size[0])' "$OPEN_SHOT")
		SCREEN_H=$(python3 -c 'from PIL import Image; import sys; print(Image.open(sys.argv[1]).size[1])' "$OPEN_SHOT")
		MENU_X=$((OPEN_X - 600)); [ "$MENU_X" -lt 0 ] && MENU_X=0
		MENU_Y=$((OPEN_Y - 760)); [ "$MENU_Y" -lt 0 ] && MENU_Y=0
		MENU_W=$((SCREEN_W - MENU_X)); MENU_H=$((SCREEN_H - MENU_Y))
		log "screen=${SCREEN_W}x${SCREEN_H} menu_region x=$MENU_X y=$MENU_Y w=$MENU_W h=$MENU_H"
		SHOT1=$OPEN_SHOT
		log "open_menu_events:"; menu_events "$OPEN_S" >>"$OUT"
		log "open_item_signals:"; item_signals "$OPEN_S" >>"$OUT"

		# Property-only change while the menu is open.
		S=$(NOW)
		timed_ctl relabel-live
		sleep 0.8
		SHOT_RELABEL="$BASE.menu-open-relabel.png"
		import -window root "$SHOT_RELABEL"
		log "relabel_menu_events:"; menu_events "$S" >>"$OUT"
		log "relabel_host_calls:"; host_calls "$S" >>"$OUT"
		log "relabel_item_signals:"; item_signals "$S" >>"$OUT"
		log "diff_open_relabel=$(python3 "$SCRIPTS/tray-open-menu.py" diff "$SHOT1" "$SHOT_RELABEL" "$MENU_X" "$MENU_Y" "$MENU_W" "$MENU_H" 2>/dev/null)"

		# Radio/checkmark state change while the menu is open.
		S=$(NOW)
		timed_ctl radio
		sleep 0.8
		SHOT_RADIO="$BASE.menu-open-radio.png"
		import -window root "$SHOT_RADIO"
		log "radio_menu_events:"; menu_events "$S" >>"$OUT"
		log "radio_item_signals:"; item_signals "$S" >>"$OUT"
		log "diff_open_radio=$(python3 "$SCRIPTS/tray-open-menu.py" diff "$SHOT_RELABEL" "$SHOT_RADIO" "$MENU_X" "$MENU_Y" "$MENU_W" "$MENU_H" 2>/dev/null)"

		# Structural change while the menu is open.
		S=$(NOW)
		timed_ctl add
		sleep 1
		SHOT_ADD="$BASE.menu-open-add.png"
		import -window root "$SHOT_ADD"
		log "add_menu_events:"; menu_events "$S" >>"$OUT"
		log "add_host_calls:"; host_calls "$S" >>"$OUT"
		log "add_host_GetLayout_count=$(host_call_count "$S" GetLayout)"
		log "diff_open_add=$(python3 "$SCRIPTS/tray-open-menu.py" diff "$SHOT_RADIO" "$SHOT_ADD" "$MENU_X" "$MENU_Y" "$MENU_W" "$MENU_H" 2>/dev/null)"
		log "closed_after_structural_add=$(menu_events "$S" | grep -c 'event=closed' || true)"

		# Open Mode and Settings (submenu parents only; no action is clicked)
		# to capture the radio/checkmark rendering in the open menu.
		OCR_REGION="$MENU_X $MENU_Y $MENU_W $MENU_H"
		MODEPOS=$(python3 "$SCRIPTS/tray-open-menu.py" find-text "$SHOT_ADD" Mode $OCR_REGION 2>&1) && MODE_RC=0 || MODE_RC=$?
		log "ocr_mode=$MODEPOS rc=$MODE_RC"
		if [ "$MODE_RC" = 0 ]; then
			MX=${MODEPOS%% *}
			REST=${MODEPOS#* }
			MY=${REST%% *}
			python3 "$SCRIPTS/tray-open-menu.py" click "$MX" "$MY"
			SHOT_MODE="$BASE.menu-open-mode-submenu.png"
			import -window root "$SHOT_MODE"
		fi
		SETTINGSPOS=$(python3 "$SCRIPTS/tray-open-menu.py" find-text "$SHOT_ADD" Settings $OCR_REGION 2>&1) && SETTINGS_RC=0 || SETTINGS_RC=$?
		log "ocr_settings=$SETTINGSPOS rc=$SETTINGS_RC"
		if [ "$SETTINGS_RC" = 0 ]; then
			SX=${SETTINGSPOS%% *}
			REST=${SETTINGSPOS#* }
			SY=${REST%% *}
			python3 "$SCRIPTS/tray-open-menu.py" click "$SX" "$SY"
			SHOT_SETTINGS="$BASE.menu-open-settings-submenu.png"
			import -window root "$SHOT_SETTINGS"
		fi

		python3 "$SCRIPTS/tray-open-menu.py" escape
		sleep 0.8
		import -window root "$BASE.after-escape.png"
		log "close_menu_events:"; menu_events "$S" >>"$OUT"
		timed_ctl remove

		# Re-open for a visual nested-submenu path: root -> Pool ->
		# Healthy (120). Only submenu parents are clicked, never an action.
		if open_menu_once submenu; then
			SHOT_ROOT=$OPEN_SHOT
			OCR_REGION="$MENU_X $MENU_Y $MENU_W $MENU_H"
			log "submenu_open_verified=yes"
			POOLPOS=$(python3 "$SCRIPTS/tray-open-menu.py" find-text "$SHOT_ROOT" Pool $OCR_REGION 2>&1) && POOL_RC=0 || POOL_RC=$?
			log "ocr_pool=$POOLPOS rc=$POOL_RC"
			if [ "$POOL_RC" = 0 ]; then
				PX=${POOLPOS%% *}
				REST=${POOLPOS#* }
				PY=${REST%% *}
				python3 "$SCRIPTS/tray-open-menu.py" click "$PX" "$PY"
				sleep 0.8
				SHOT_POOL="$BASE.submenu-pool.png"
				import -window root "$SHOT_POOL"
				HEALTHYPOS=$(python3 "$SCRIPTS/tray-open-menu.py" find-text "$SHOT_POOL" Healthy $OCR_REGION 2>&1) && HEALTHY_RC=0 || HEALTHY_RC=$?
				log "ocr_healthy=$HEALTHYPOS rc=$HEALTHY_RC"
				# A screenshot between two clicks closes the open submenu on
				# this host, so the second level is opened immediately using
				# the row offset observed in the first level (Pool -> Healthy).
				HX=$((PX - 447))
				HY=$((PY + 35))
				if [ "$HEALTHY_RC" = 0 ]; then
					HX=${HEALTHYPOS%% *}
					REST=${HEALTHYPOS#* }
					HY=${REST%% *}
				fi
				log "click_healthy x=$HX y=$HY source=$([ "$HEALTHY_RC" = 0 ] && echo ocr || echo offset)"
				python3 "$SCRIPTS/tray-open-menu.py" click "$HX" "$HY"
				sleep 0.9
				SHOT_HEALTHY="$BASE.submenu-healthy-page1.png"
				import -window root "$SHOT_HEALTHY"
				log "ocr_page_label=$(python3 "$SCRIPTS/tray-open-menu.py" find-text "$SHOT_HEALTHY" Page $OCR_REGION 2>&1)"
				log "ocr_healthy_row=$(python3 "$SCRIPTS/tray-open-menu.py" find-text "$SHOT_HEALTHY" healthy $OCR_REGION 2>&1)"
			fi
			python3 "$SCRIPTS/tray-open-menu.py" escape
			sleep 0.5
			import -window root "$BASE.submenu-closed.png"
		else
			log "submenu_open_verified=no"
		fi
	else
		OPEN=misclick
		log "open_verified_by_host=no_AboutToShow_after_3_attempts"
	fi
else
	log "open_test=skipped reason=missing_import_or_python_modules"
fi
log "open_test=$OPEN"

section "cleanup"
kill "$APP" 2>/dev/null
wait "$APP" 2>/dev/null
sleep 0.5
log "spike_alive_after_kill=$(pgrep -f 'menu-spike-amd64.*routelatch-tray-menu-spike' | wc -l)"
log "watcher_still_lists_item=$(gdbus call --session --dest org.kde.StatusNotifierWatcher --object-path /StatusNotifierWatcher \
	--method org.freedesktop.DBus.Properties.Get org.kde.StatusNotifierWatcher RegisteredStatusNotifierItems 2>/dev/null | grep -c "$BUS")"
log "spike_log=$BASE.spike.log"
log "monitor_log=$MON"
log "initial_layout=$INITIAL"
