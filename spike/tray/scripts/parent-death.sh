#!/bin/sh
# Parent-death / child-exit measurement for the tray spike.
#
# Usage: scripts/parent-death.sh [build-dir]
# Requires the binaries built by `go build ./cmd/...` (see FINDINGS.md).
# Touches no desktop state; run it in an isolated session bus or without one.
set -u

BUILD=${1:-/tmp/opencode/tray-build}
OUT=${OUT:-/tmp/opencode/tray-evidence/parent-death.txt}
mkdir -p "$(dirname "$OUT")"
: >"$OUT"

log() { printf '%s\n' "$*" >>"$OUT"; }

# wait_gone <pid> <iterations>: 0 when /proc/<pid> disappeared, 1 on timeout.
# Always advances, so a persistent zombie is reported instead of looping.
wait_gone() {
	n=0
	ZOMBIE=no
	while [ "$n" -lt "$2" ]; do
		if [ ! -e "/proc/$1" ]; then
			return 0
		fi
		state=$(awk '{print $3}' "/proc/$1/stat" 2>/dev/null)
		if [ "$state" = Z ]; then
			ZOMBIE=yes
		fi
		n=$((n + 1))
		sleep 0.005
	done
	return 1
}

# --- case 1: supervisor is SIGKILLed, child must exit on its own -----------
"$BUILD/supervisor-amd64" "$BUILD/lifecycle-child-amd64" -watch-parent >"$OUT.case1.log" 2>&1 &
SUP=$!
i=0
while [ $i -lt 200 ]; do
	grep -q 'parent_watch_armed' "$OUT.case1.log" 2>/dev/null && break
	i=$((i + 1))
	sleep 0.01
done
CHILD=$(sed -n 's/.*event=supervisor_ready pid=[0-9]* child_pid=\([0-9]*\).*/\1/p' "$OUT.case1.log" | head -1)
if [ -z "${CHILD:-}" ]; then
	log "case1 FAIL: no child pid; log:"
	cat "$OUT.case1.log" >>"$OUT"
	exit 1
fi
log "case1 supervisor=$SUP child=$CHILD"
T0=$(date +%s%N)
kill -9 "$SUP"
if wait_gone "$CHILD" 1000; then STATUS=gone; else STATUS=timeout; kill -9 "$CHILD" 2>/dev/null; fi
T1=$(date +%s%N)
wait "$SUP" 2>/dev/null
log "case1 result=$STATUS child_after_supervisor_sigkill_ms=$(( (T1 - T0) / 1000000 )) zombie_seen=$ZOMBIE"
log "case1 child log:"
sed 's/^/  /' "$OUT.case1.log" >>"$OUT"

# --- case 2: child exits by itself, supervisor must detect it --------------
"$BUILD/supervisor-amd64" "$BUILD/lifecycle-child-amd64" -seconds 2 >"$OUT.case2.log" 2>&1 &
SUP2=$!
wait "$SUP2"
CODE=$?
log "case2 supervisor_exit_code=$CODE child_exited_lines=$(grep -c 'event=child_exited' "$OUT.case2.log")"
log "case2 child log:"
sed 's/^/  /' "$OUT.case2.log" >>"$OUT"

# --- case 3: supervisor dies before the child arms the watchdog ------------
# The supervisor passes a pidfd on itself, so a late arm must still observe the
# death instead of watching the reparented process.
"$BUILD/supervisor-amd64" -abandon-after=50ms "$BUILD/lifecycle-child-amd64" \
	-watch-parent -arm-delay=500ms >"$OUT.case3.log" 2>&1 &
SUP3=$!
wait "$SUP3"
T0=$(date +%s%N)
CHILD3=$(sed -n 's/.*event=supervisor_ready pid=[0-9]* child_pid=\([0-9]*\).*/\1/p' "$OUT.case3.log" | head -1)
if [ -z "${CHILD3:-}" ]; then
	log "case3 FAIL: no child pid; log:"
	cat "$OUT.case3.log" >>"$OUT"
	exit 1
fi
if wait_gone "$CHILD3" 1000; then STATUS3=gone; else STATUS3=timeout; kill -9 "$CHILD3" 2>/dev/null; fi
T1=$(date +%s%N)
log "case3 (arm-late race) child_after_supervisor_exit_ms=$(( (T1 - T0) / 1000000 )) result=$STATUS3 zombie_seen=$ZOMBIE"
log "case3 child log:"
sed 's/^/  /' "$OUT.case3.log" >>"$OUT"

echo "wrote $OUT"
