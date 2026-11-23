#!/bin/sh
# Live KDE D-Bus evidence for one tray prototype.
#
# Usage: scripts/kde-evidence.sh <binary> <tag> [output-dir]
# Requires an existing KDE session bus with org.kde.StatusNotifierWatcher.
# It creates exactly one tray item with the spike app id, sends notifications
# labelled as spike traffic, clicks two menu entries through com.canonical.dbusmenu,
# and removes the item again. No desktop setting is read or written.
set -u

BIN=$1
TAG=$2
OUT=${3:-/tmp/opencode/tray-evidence}
BUSADDR=${DBUS_SESSION_BUS_ADDRESS:?set DBUS_SESSION_BUS_ADDRESS to the KDE session bus}
mkdir -p "$OUT"
BASE="$OUT/kde-$TAG"
: >"$BASE.log"

export DBUS_SESSION_BUS_ADDRESS="$BUSADDR"
log() { printf '%s\n' "$*" >>"$BASE.log"; }

dbus-monitor --session "type='method_call',interface='org.freedesktop.Notifications',member='Notify'" >"$BASE.notify-monitor.log" 2>&1 &
MON=$!
"$BIN" -id routelatch-tray-spike -notify -seconds 20 >>"$BASE.log" 2>&1 &
APP=$!
cleanup() {
	kill "$APP" 2>/dev/null
	kill "$MON" 2>/dev/null
	wait "$APP" 2>/dev/null
	wait "$MON" 2>/dev/null
}
trap cleanup EXIT INT TERM

i=0
while [ $i -lt 300 ]; do
	grep -q 'event=tray_registered' "$BASE.log" 2>/dev/null && break
	i=$((i + 1))
	sleep 0.01
done
ITEM=$(sed -n 's/.*item=\([^ ]*\).*/\1/p' "$BASE.log" | head -1)
if [ -z "${ITEM:-}" ]; then
	log "FAIL: no tray_registered line"
	cat "$BASE.log"
	exit 1
fi
BUS=${ITEM%%/*}
log "item=$ITEM bus=$BUS"

get() {
	gdbus call --session --dest "$BUS" --object-path /StatusNotifierItem \
		--method org.freedesktop.DBus.Properties.Get "$1" "$2"
}
log "property Id: $(get org.kde.StatusNotifierItem Id)"
log "property Title: $(get org.kde.StatusNotifierItem Title)"
log "property Status: $(get org.kde.StatusNotifierItem Status)"
PIXMAP=$(get org.kde.StatusNotifierItem IconPixmap)
log "IconPixmap bytes_in_reply=$(printf '%s' "$PIXMAP" | wc -c) dimensions_22x22=$(printf '%s' "$PIXMAP" | grep -c '(22, 22,')"
MENUPATH=$(gdbus call --session --dest "$BUS" --object-path /StatusNotifierItem \
	--method org.freedesktop.DBus.Properties.Get org.kde.StatusNotifierItem Menu |
	sed -n "s/.*'\([^']*\)'.*/\1/p")
log "menu_path=$MENUPATH"
LAYOUT=$(gdbus call --session --dest "$BUS" --object-path "$MENUPATH" \
	--method com.canonical.dbusmenu.GetLayout -- 0 -1 "[]")
log "menu_layout=$LAYOUT"

NOTIFY_ID=$(printf '%s' "$LAYOUT" | sed -n "s/.*(\([0-9]*\), {'label': <'Send test notification'>.*/\1/p")
QUIT_ID=$(printf '%s' "$LAYOUT" | sed -n "s/.*(\([0-9]*\), {'label': <'Quit prototype'>.*/\1/p")
if [ -z "$NOTIFY_ID" ] || [ -z "$QUIT_ID" ]; then
	log "FAIL: could not find menu ids"
	exit 1
fi
log "click notify id=$NOTIFY_ID: $(busctl --user call "$BUS" "$MENUPATH" com.canonical.dbusmenu Event isvu "$NOTIFY_ID" clicked v i 0 0 2>&1)"
sleep 1
log "click quit id=$QUIT_ID: $(busctl --user call "$BUS" "$MENUPATH" com.canonical.dbusmenu Event isvu "$QUIT_ID" clicked v i 0 0 2>&1)"
i=0
while [ $i -lt 300 ]; do
	kill -0 "$APP" 2>/dev/null || break
	i=$((i + 1))
	sleep 0.01
done
ALIVE=no
kill -0 "$APP" 2>/dev/null && ALIVE=yes
log "after_quit_event process_alive=$ALIVE"
EXITED_AFTER_MENU=$([ "$ALIVE" = no ] && echo yes || echo no)
log "notify_calls_seen=$(grep -c 'member=Notify' "$BASE.notify-monitor.log" 2>/dev/null || echo 0)"
if grep -q 'routelatch-tray-spike' "$BASE.notify-monitor.log" 2>/dev/null; then
	NOTIF_OK=yes
else
	NOTIF_OK=no
fi
ITEM_STILL=$(gdbus call --session --dest org.kde.StatusNotifierWatcher --object-path /StatusNotifierWatcher \
	--method org.freedesktop.DBus.Properties.Get org.kde.StatusNotifierWatcher RegisteredStatusNotifierItems 2>/dev/null | grep -c "$BUS")
log "watcher_still_lists_item=$ITEM_STILL notification_observed=$NOTIF_OK exited_after_menu=$EXITED_AFTER_MENU"
if [ "$ALIVE" = yes ]; then
	log "FAIL: process still alive after Quit event"
	exit 1
fi
exit 0
