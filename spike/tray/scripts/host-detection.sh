#!/bin/sh
# Watcher/host detection on an isolated bus, using the fake watcher.
# Usage: scripts/host-detection.sh [build-dir]
set -u
BUILD=${1:-/tmp/opencode/tray-build}
OUT=${OUT:-/tmp/opencode/tray-evidence/host-detection.txt}
: >"$OUT"
log() { printf '%s\n' "$*" >>"$OUT"; }

run_case() {
	host=$1
	shift
	"$BUILD/fake-watcher-amd64" -host="$host" >>"$OUT.fake.log" 2>&1 &
	FW=$!
	sleep 0.4
	for bin in "$@"; do
		name=$(basename "$bin")
		"$bin" -check-watcher >>"$OUT" 2>&1
		log "case host=$host $name check-watcher exit=$?"
	done
	kill "$FW" 2>/dev/null
	wait "$FW" 2>/dev/null
}

log "== no watcher (only the fresh session bus)"
for bin in "$BUILD/systray-spike-amd64" "$BUILD/tray-spike-amd64"; do
	"$bin" -check-watcher >>"$OUT" 2>&1
	log "case host=absent $(basename "$bin") exit=$?"
done

log "== watcher present, host absent"
run_case false "$BUILD/systray-spike-amd64" "$BUILD/tray-spike-amd64"

log "== watcher present, host present"
run_case true "$BUILD/systray-spike-amd64" "$BUILD/tray-spike-amd64"
cat "$OUT"
