#!/bin/sh
# Build the Linux release binaries for RouteLatch.
#
# This script only writes into ./dist. It does not install, enable a service,
# change desktop/network settings or publish anything.
#
# Usage:
#   scripts/build.sh                 # build linux/amd64 and linux/arm64
#   scripts/build.sh --reproducible  # build twice and compare checksums
#
# Inputs that make the build reproducible: same Go toolchain (see go.mod),
# same source tree, same VERSION/COMMIT, and SOURCE_DATE_EPOCH for the date.
# The script itself fetches nothing; `go build` uses the module cache and would
# download a missing pinned module.
set -eu

cd "$(dirname "$0")/.."
repo=$(pwd)
dist="$repo/dist"

VERSION=${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}
COMMIT=${COMMIT:-$(git rev-parse --short HEAD 2>/dev/null || echo unknown)}
if [ -n "${SOURCE_DATE_EPOCH:-}" ]; then
	DATE=$(date -u -d "@$SOURCE_DATE_EPOCH" +%Y-%m-%dT%H:%M:%SZ)
else
	DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ)
fi
# The stamped binary must not keep the "dev" default; refuse to build without a
# real version/commit so the check below cannot pass on the fallback values.
if [ "$VERSION" = dev ] || [ "$COMMIT" = unknown ]; then
	echo "cannot stamp a build: set VERSION and COMMIT explicitly (no git metadata found)" >&2
	exit 1
fi

# -X symbols are declared in cmd/routelatch/version.go as main.version,
# main.commit and main.buildDate; `routelatch version` prints them. The names
# must match exactly: the Go linker ignores -X for an unknown symbol silently,
# so build_all verifies the result instead of trusting the flags.
LDFLAGS="-s -w -X main.version=$VERSION -X main.commit=$COMMIT -X main.buildDate=$DATE"
GOVER=$(go version | awk '{print $3}')
host_arch=$(go env GOHOSTARCH)

build_arch() {
	arch=$1
	out="$2"
	mkdir -p "$(dirname "$out")"
	echo "building linux/$arch -> ${out#"$repo"/}"
	CGO_ENABLED=0 GOOS=linux GOARCH=$arch \
		go build -mod=readonly -buildvcs=false -trimpath \
		-ldflags "$LDFLAGS" -o "$out" ./cmd/routelatch
}

stamp_failed() {
	echo "stamp check FAILED: $*" >&2
	exit 1
}

# assert_stamp proves the linker consumed the -X values. Both architectures must
# embed the three strings; the target that matches this host must also report
# them through `version` and `--json version`. A binary still reporting the
# `dev` default means a -X path or name no longer matches and fails the build.
assert_stamp() {
	arch=$1
	out=$2
	for value in "$VERSION" "$COMMIT" "$DATE"; do
		grep -qaF -- "$value" "$out" ||
			stamp_failed "$out does not embed '$value'"
	done
	if [ "$arch" != "$host_arch" ]; then
		echo "stamp linux/$arch: version/commit/date embedded (not executable on $host_arch)"
		return 0
	fi
	human=$("$out" version)
	printf '%s\n' "$human"
	case $human in
	*"RouteLatch $VERSION"*) ;;
	*) stamp_failed "version output does not report '$VERSION'" ;;
	esac
	case $human in
	*"commit: $COMMIT"*) ;;
	*) stamp_failed "version output does not report commit '$COMMIT'" ;;
	esac
	case $human in
	*"build date: $DATE"*) ;;
	*) stamp_failed "version output does not report date '$DATE'" ;;
	esac
	json=$("$out" --json version)
	printf '%s\n' "$json"
	json_version=$(printf '%s\n' "$json" | sed -n 's/.*"version":"\([^"]*\)".*/\1/p')
	[ "$json_version" = "$VERSION" ] ||
		stamp_failed "--json version reports '$json_version', expected '$VERSION'"
	for pair in "commit:$COMMIT" "build_date:$DATE"; do
		key=${pair%%:*}
		want=${pair#*:}
		got=$(printf '%s\n' "$json" | sed -n "s/.*\"$key\":\"\\([^\"]*\\)\".*/\\1/p")
		[ "$got" = "$want" ] ||
			stamp_failed "--json $key reports '$got', expected '$want'"
	done
	echo "stamp linux/$arch: proven via version and --json"
}

build_all() {
	root=$1
	build_arch amd64 "$root/routelatch-linux-amd64"
	build_arch arm64 "$root/routelatch-linux-arm64"
	assert_stamp amd64 "$root/routelatch-linux-amd64"
	assert_stamp arm64 "$root/routelatch-linux-arm64"
	( cd "$root" && sha256sum routelatch-linux-amd64 routelatch-linux-arm64 >SHA256SUMS )
}

# BUILD-INFO is the readable stamp that ships beside the binaries; the same
# values are compiled in and printable with `routelatch version`.
write_build_info() {
	root=$1
	cat >"$root/BUILD-INFO" <<EOF
version=$VERSION
commit=$COMMIT
date=$DATE
go=$GOVER
cgo=0
source_date_epoch=${SOURCE_DATE_EPOCH:-}
EOF
}

case "${1:-}" in
--reproducible)
	tmp=$(mktemp -d "${TMPDIR:-/tmp}/routelatch-repro.XXXXXX")
	trap 'rm -rf "$tmp"' EXIT INT TERM
	echo "version=$VERSION commit=$COMMIT date=$DATE"
	build_all "$tmp/a"
	build_all "$tmp/b"
	if diff -u "$tmp/a/SHA256SUMS" "$tmp/b/SHA256SUMS"; then
		echo "reproducible: two builds with identical inputs produced identical bytes"
	else
		echo "NOT reproducible: checksums differ between two builds" >&2
		exit 1
	fi
	;;
"")
	rm -rf "$dist"
	mkdir -p "$dist"
	echo "version=$VERSION commit=$COMMIT date=$DATE"
	build_all "$dist"
	write_build_info "$dist"
	echo
	echo "wrote $dist/SHA256SUMS:"
	cat "$dist/SHA256SUMS"
	echo
	echo "wrote $dist/BUILD-INFO (version/commit/date stamp that the package ships)"
	echo "install or enable nothing here; packaging/README.md documents the user's steps."
	;;
*)
	echo "usage: $0 [--reproducible]" >&2
	exit 2
	;;
esac
