#!/bin/sh
# Verify that a built RouteLatch binary is self-contained, carries its build
# stamp, and starts on a clean environment. It does not install or enable
# anything: it runs `version` with an empty environment, then starts `run` in the
# background with temporary XDG directories and loopback-only listeners, asks
# `status`, and stops it with SIGTERM.
#
# Usage: scripts/check-artifact.sh [dist/routelatch-linux-amd64]
#
# The expected stamp comes from VERSION/COMMIT/BUILD_DATE in the environment, or
# from dist/BUILD-INFO, which scripts/build.sh writes. Exit status is non-zero
# if any check fails.
set -eu

cd "$(dirname "$0")/.."
repo=$(pwd)
dist=${DIST:-$repo/dist}
artifact=${1:-$dist/routelatch-linux-amd64}
http_port=${ROUTELATCH_CHECK_HTTP_PORT:-18317}
socks_port=${ROUTELATCH_CHECK_SOCKS_PORT:-18318}

if [ ! -x "$artifact" ]; then
	echo "missing or non-executable artifact: $artifact" >&2
	exit 2
fi

info="$dist/BUILD-INFO"
version=${VERSION:-$(sed -n 's/^version=//p' "$info" 2>/dev/null || true)}
commit=${COMMIT:-$(sed -n 's/^commit=//p' "$info" 2>/dev/null || true)}
build_date=${BUILD_DATE:-$(sed -n 's/^date=//p' "$info" 2>/dev/null || true)}
if [ -z "$version" ] || [ -z "$commit" ] || [ -z "$build_date" ]; then
	echo "cannot determine the expected stamp; run scripts/build.sh or set VERSION, COMMIT and BUILD_DATE" >&2
	exit 2
fi

echo "== file =="
file "$artifact"

echo "== dynamic linkage =="
if ldd "$artifact" 2>&1 | grep -q "not a dynamic executable"; then
	echo "static: ldd reports no dynamic executable"
elif command -v readelf >/dev/null 2>&1 && ! readelf -l "$artifact" 2>/dev/null | grep -q INTERP; then
	echo "static: no PT_INTERP program header"
else
	echo "NOT static:" >&2
	ldd "$artifact" >&2 || true
	exit 1
fi

echo "== build stamp: expected version=$version commit=$commit build_date=$build_date =="
human=$(env -i "$artifact" version)
printf '%s\n' "$human"
case $human in
*"RouteLatch $version"*) ;;
*) echo "version output does not report '$version'" >&2; exit 1 ;;
esac
case $human in
*"commit: $commit"*) ;;
*) echo "version output does not report commit '$commit'" >&2; exit 1 ;;
esac
case $human in
*"build date: $build_date"*) ;;
*) echo "version output does not report build date '$build_date'" >&2; exit 1 ;;
esac

json=$(env -i "$artifact" --json version)
printf '%s\n' "$json"
json_version=$(printf '%s\n' "$json" | sed -n 's/.*"version":"\([^"]*\)".*/\1/p')
json_commit=$(printf '%s\n' "$json" | sed -n 's/.*"commit":"\([^"]*\)".*/\1/p')
json_date=$(printf '%s\n' "$json" | sed -n 's/.*"build_date":"\([^"]*\)".*/\1/p')
[ "$json_version" = "$version" ] || { echo "--json version is '$json_version', expected '$version'" >&2; exit 1; }
[ "$json_commit" = "$commit" ] || { echo "--json commit is '$json_commit', expected '$commit'" >&2; exit 1; }
[ "$json_date" = "$build_date" ] || { echo "--json build_date is '$json_date', expected '$build_date'" >&2; exit 1; }
echo "stamp: version, commit and build_date match in human and --json output"

echo "== empty environment, temporary XDG, loopback-only listeners =="
tmp=$(mktemp -d "${TMPDIR:-/tmp}/routelatch-check.XXXXXX")
chmod 700 "$tmp"
pid=""
cleanup() {
	if [ -n "$pid" ]; then
		kill -TERM "$pid" 2>/dev/null || true
		wait "$pid" 2>/dev/null || true
	fi
	rm -rf "$tmp"
}
trap cleanup EXIT INT TERM

mkdir -p "$tmp/config/routelatch" "$tmp/state" "$tmp/home"
chmod 700 "$tmp/config" "$tmp/config/routelatch" "$tmp/state" "$tmp/home"
cat >"$tmp/config/routelatch/config.json" <<EOF
{"schema_version":1,"http_listen":"127.0.0.1:$http_port","socks_listen":"127.0.0.1:$socks_port","log_level":"info"}
EOF
chmod 600 "$tmp/config/routelatch/config.json"

socket="$tmp/state/routelatch/run/control.sock"
env -i XDG_CONFIG_HOME="$tmp/config" XDG_STATE_HOME="$tmp/state" HOME="$tmp/home" \
	"$artifact" run >"$tmp/run.log" 2>&1 &
pid=$!

tries=0
while [ ! -S "$socket" ] && [ "$tries" -lt 100 ]; do
	if ! kill -0 "$pid" 2>/dev/null; then
		echo "run exited before the socket appeared:" >&2
		cat "$tmp/run.log" >&2
		exit 1
	fi
	sleep 0.1
	tries=$((tries + 1))
done
if [ ! -S "$socket" ]; then
	echo "management socket did not appear at $socket" >&2
	cat "$tmp/run.log" >&2
	exit 1
fi

code=0
env -i XDG_CONFIG_HOME="$tmp/config" XDG_STATE_HOME="$tmp/state" HOME="$tmp/home" \
	"$artifact" status || code=$?
if [ "$code" -ne 0 ]; then
	echo "status exited $code" >&2
	cat "$tmp/run.log" >&2
	exit 1
fi

kill -TERM "$pid"
wait "$pid"
run_exit=$?
pid=""
echo "run exit after SIGTERM: $run_exit"
[ "$run_exit" -eq 0 ] || exit 1
echo "PASS: artifact is static, stamped, and answers version/status with an empty environment"
