#!/bin/sh
# Assemble and verify the file set a RouteLatch GitHub Release publishes.
#
# The release workflow builds the 12 package artifacts (four tarballs, four
# .deb files, four RPM specs and SHA256SUMS) with scripts/package.sh, and the
# four binary RPMs from those specs. This helper joins them into ONE directory,
# extends SHA256SUMS to the 16 files the release publishes and verifies the
# exact file set and digests. The assembly job and the publishing job run the
# same verification, so a missing, renamed or mismatched asset cannot pass.
#
# This script installs and publishes nothing; it only reads, copies and hashes
# files in the directories it is given.
#
# Usage:
#   VERSION=v1.0.0 scripts/release-assets.sh assemble <packages-dir> <rpms-dir> <out-dir>
#   VERSION=v1.0.0 scripts/release-assets.sh verify <assets-dir>
#
# VERSION is the build version (the tag name for a real release, a synthetic
# version for a dry run). ARCHES defaults to the four supported architectures.
set -eu
umask 022

arches=${ARCHES:-"amd64 arm64 riscv64 ppc64le"}
version=${VERSION:-}

usage() {
	echo "usage: VERSION=<version> $0 assemble <packages-dir> <rpms-dir> <out-dir>" >&2
	echo "       VERSION=<version> $0 verify <assets-dir>" >&2
	exit 2
}

die() {
	echo "release assets FAILED: $*" >&2
	exit 1
}

[ -n "$version" ] || die "set VERSION to the release version (for example v1.0.0)"

# Must match scripts/package.sh and scripts/check-package.sh.
deb_arch_for() {
	case $1 in
	ppc64le) echo ppc64el ;;
	*) echo "$1" ;;
	esac
}
rpm_arch_for() {
	case $1 in
	amd64) echo x86_64 ;;
	arm64) echo aarch64 ;;
	*) echo "$1" ;;
	esac
}

tmp=$(mktemp -d "${TMPDIR:-/tmp}/routelatch-release.XXXXXX")
trap 'rm -rf "$tmp"' EXIT INT TERM

# The generated x86_64 spec carries the one normalised package version that the
# .deb files and every RPM use, so their names are derived from it rather than
# re-normalising VERSION here.
package_version() {
	dir=$1
	spec="$dir/routelatch-$(rpm_arch_for amd64).spec"
	[ -f "$spec" ] || die "missing $spec"
	sed -n 's/^Version:[[:space:]]*//p' "$spec"
}

# The 16 published files, one per line.
expected_names() {
	dir=$1
	pkg_version=$(package_version "$dir")
	[ -n "$pkg_version" ] || die "cannot read the package version from $dir"
	for arch in $arches; do
		deb_arch=$(deb_arch_for "$arch")
		rpm_arch=$(rpm_arch_for "$arch")
		printf '%s\n' \
			"routelatch-$version-linux-$arch.tar.gz" \
			"routelatch_${pkg_version}_${deb_arch}.deb" \
			"routelatch-$rpm_arch.spec" \
			"routelatch-$pkg_version-1.$rpm_arch.rpm"
	done
}

check_names() {
	dir=$1
	# shellcheck disable=SC2035
	awk '{print $2}' "$dir/SHA256SUMS" | LC_ALL=C sort >"$tmp/sums.names"
	expected_names "$dir" | LC_ALL=C sort >"$tmp/expected.names"
	diff -u "$tmp/expected.names" "$tmp/sums.names" ||
		die "$dir/SHA256SUMS does not list exactly the expected files"
	[ "$(wc -l <"$dir/SHA256SUMS")" -eq 16 ] ||
		die "$dir/SHA256SUMS must have 16 entries"
}

verify() {
	dir=$1
	[ -d "$dir" ] || die "missing assets directory $dir"
	[ -f "$dir/SHA256SUMS" ] || die "missing $dir/SHA256SUMS"
	check_names "$dir"
	( cd "$dir" && sha256sum -c SHA256SUMS ) ||
		die "a checksum in $dir/SHA256SUMS failed"
	find "$dir" -maxdepth 1 -type f -printf '%f\n' | LC_ALL=C sort >"$tmp/actual.files"
	{
		expected_names "$dir"
		printf 'SHA256SUMS\n'
	} | LC_ALL=C sort >"$tmp/expected.files"
	diff -u "$tmp/expected.files" "$tmp/actual.files" ||
		die "$dir does not hold exactly the 17 expected files"
	echo "PASS: 16 checksummed files and SHA256SUMS ($(wc -l <"$dir/SHA256SUMS") entries) verified in $dir"
}

assemble() {
	packages=$1
	rpms=$2
	out=$3
	[ -d "$packages" ] || die "missing packages directory $packages"
	[ -d "$rpms" ] || die "missing RPM directory $rpms"

	rm -rf "$out"
	mkdir -p "$out"
	for file in "$packages"/*; do
		[ -f "$file" ] || continue
		case $(basename "$file") in
		SHA256SUMS | *.tar.gz | *.deb | *.spec) cp -p "$file" "$out/" ;;
		*) die "unexpected file $file in $packages" ;;
		esac
	done
	[ "$(wc -l <"$out/SHA256SUMS")" -eq 12 ] ||
		die "package SHA256SUMS must have 12 entries before the RPMs are added"
	# The 12 package entries must be verified against the files actually built
	# before their checksum file is extended.
	expected_names "$out" | grep -v '\.rpm$' | LC_ALL=C sort >"$tmp/package.names"
	awk '{print $2}' "$out/SHA256SUMS" | LC_ALL=C sort >"$tmp/package.actual"
	diff -u "$tmp/package.names" "$tmp/package.actual" ||
		die "package SHA256SUMS does not list the 12 built package files"
	( cd "$out" && sha256sum -c SHA256SUMS ) ||
		die "a package checksum failed before the RPMs were added"

	rpm_count=0
	for file in "$rpms"/*.rpm; do
		[ -f "$file" ] || continue
		rpm_count=$((rpm_count + 1))
		cp -p "$file" "$out/"
	done
	[ "$rpm_count" -eq 4 ] || die "expected 4 binary RPMs in $rpms, found $rpm_count"
	(
		cd "$out"
		for file in *.rpm; do
			sha256sum "$file"
		done >>SHA256SUMS
	)

	verify "$out"
}

case ${1:-} in
assemble)
	[ $# -eq 4 ] || usage
	assemble "$2" "$3" "$4"
	;;
verify)
	[ $# -eq 2 ] || usage
	verify "$2"
	;;
*) usage ;;
esac
