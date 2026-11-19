#!/bin/sh
# Verify the packages scripts/package.sh assembled: the exact checksum member
# set, tarball layout, .deb control metadata (including Debian architecture
# names), control archive members, payload file set, modes and the binary bytes
# in both containers.
#
# It reads BUILD-INFO for the expected version and never installs anything.
# Usage: scripts/check-package.sh [dist/packages]
#        DIST=/path/to/dist scripts/check-package.sh
set -eu

cd "$(dirname "$0")/.."
repo=$(pwd)
dist=${DIST:-$repo/dist}
stage=${1:-$dist/packages}
info="$dist/BUILD-INFO"
arches=${ARCHES:-"amd64 arm64 riscv64 ppc64le"}

fail() {
	echo "package check FAILED: $*" >&2
	exit 1
}

tmp=$(mktemp -d "${TMPDIR:-/tmp}/routelatch-check.XXXXXX")
trap 'rm -rf "$tmp"' EXIT INT TERM

[ -f "$info" ] || fail "missing $info; run scripts/build.sh first"
[ -d "$stage" ] || fail "missing package directory $stage"
[ -f "$stage/SHA256SUMS" ] || fail "missing $stage/SHA256SUMS"
command -v dpkg-deb >/dev/null 2>&1 || fail "dpkg-deb is required to inspect .deb files"

version=$(sed -n 's/^version=//p' "$info")
[ -n "$version" ] || fail "BUILD-INFO has no version"
# Must match scripts/package.sh: strip a leading v, then prefix a value that
# would not start with a digit so it is a valid Debian version.
deb_version=$(printf '%s' "$version" | sed 's/^v//')
case $deb_version in
[0-9]*) ;;
*) deb_version="0.0.0+$deb_version" ;;
esac

# Must match scripts/package.sh: Debian calls little-endian 64-bit PowerPC
# "ppc64el" while Go calls it "ppc64le".
deb_arch_for() {
	case $1 in
	ppc64le) echo ppc64el ;;
	*) echo "$1" ;;
	esac
}

check_deb_arch() {
	if command -v dpkg-architecture >/dev/null 2>&1; then
		dpkg-architecture -a"$1" -qDEB_HOST_ARCH >/dev/null 2>&1 ||
			fail "$1 is not a Debian architecture"
	elif [ -r /usr/share/dpkg/cputable ]; then
		grep -Eq "^$1[[:space:]]" /usr/share/dpkg/cputable ||
			fail "$1 is not a Debian architecture"
	else
		echo "note: cannot validate Debian architecture names on this host" >&2
	fi
}

# The checksum file is the only trust anchor, so it must list exactly the
# expected artifacts: sha256sum -c alone would not notice a missing line.
: >"$tmp/sums.expected"
for arch in $arches; do
	printf '%s\n' \
		"routelatch-$version-linux-$arch.tar.gz" \
		"routelatch_${deb_version}_$(deb_arch_for "$arch").deb" >>"$tmp/sums.expected"
done
sort -o "$tmp/sums.expected" "$tmp/sums.expected"
awk '{print $2}' "$stage/SHA256SUMS" | sort >"$tmp/sums.actual"
diff -u "$tmp/sums.expected" "$tmp/sums.actual" ||
	fail "$stage/SHA256SUMS does not list exactly the expected artifacts"

echo "== checksums in $stage/SHA256SUMS =="
( cd "$stage" && sha256sum -c SHA256SUMS )

expect_file() {
	printf '%s\n' "$1" | grep -Fxq "$2" || fail "$1 is missing from $3"
}

for arch in $arches; do
	name="routelatch-$version-linux-$arch"
	deb_arch=$(deb_arch_for "$arch")
	check_deb_arch "$deb_arch"
	tar="$stage/$name.tar.gz"
	deb="$stage/routelatch_${deb_version}_${deb_arch}.deb"
	[ -f "$tar" ] || fail "missing $tar"
	[ -f "$deb" ] || fail "missing $deb (did package.sh run with dpkg-deb?)"
	dist_bin="$dist/routelatch-linux-$arch"
	[ -f "$dist_bin" ] || fail "missing $dist_bin"

	echo "== linux/$arch tarball =="
	tar -tzf "$tar" >/dev/null || fail "$tar is not a readable gzip tar"
	tar_files=$(tar -tzf "$tar")
	if printf '%s\n' "$tar_files" | grep -Eq '(^|/)DEBIAN(/|$)'; then
		fail "$tar contains a DEBIAN directory"
	fi
	for path in \
		bin/routelatch \
		share/doc/routelatch/README.md \
		share/doc/routelatch/INSTALL.md \
		share/doc/routelatch/SERVICE.md \
		share/doc/routelatch/LICENSE \
		share/doc/routelatch/NOTICE \
		share/doc/routelatch/THIRD-PARTY-NOTICES.md \
		share/doc/routelatch/docs/licensing.md \
		share/doc/routelatch/go.sum \
		share/doc/routelatch/BUILD-INFO \
		share/doc/routelatch/PACKAGE-NOTES \
		share/routelatch/systemd/routelatch.service; do
		expect_file "$tar_files" "$name/$path" "$tar"
	done
	tar_bin_hash=$(tar -xOzf "$tar" "$name/bin/routelatch" | sha256sum | awk '{print $1}')
	dist_bin_hash=$(sha256sum "$dist_bin" | awk '{print $1}')
	[ "$tar_bin_hash" = "$dist_bin_hash" ] ||
		fail "$tar bin/routelatch does not match $dist_bin"

	sidecar=0
	if printf '%s\n' "$tar_files" | grep -Fxq "$name/bin/sing-box"; then
		sidecar=1
		expect_file "$tar_files" "$name/share/doc/routelatch/SINGBOX-INFO" "$tar"
	fi

	echo "== linux/$arch .deb metadata =="
	[ "$(dpkg-deb -f "$deb" Package)" = routelatch ] || fail "$deb Package is not routelatch"
	[ "$(dpkg-deb -f "$deb" Version)" = "$deb_version" ] ||
		fail "$deb Version is not $deb_version"
	[ "$(dpkg-deb -f "$deb" Architecture)" = "$deb_arch" ] ||
		fail "$deb Architecture is not $deb_arch"
	depends=$(dpkg-deb -f "$deb" Depends)
	case " $depends " in
	*" ca-certificates "*) ;;
	*) fail "$deb does not depend on ca-certificates (got '$depends')" ;;
	esac
	description=$(dpkg-deb -f "$deb" Description)
	printf '%s\n' "$description" | grep -q '^targeted proxy client for Linux$' ||
		fail "$deb has an unexpected Description"
	[ -n "$(dpkg-deb -f "$deb" Maintainer)" ] || fail "$deb has no Maintainer"
	[ -n "$(dpkg-deb -f "$deb" Installed-Size)" ] || fail "$deb has no Installed-Size"

	echo "== linux/$arch .deb control archive =="
	ctrl_files=$(dpkg-deb --ctrl-tarfile "$deb" | tar -tf - | sed 's|^\./||')
	for member in $ctrl_files; do
		case $member in
		control | md5sums) ;;
		*) fail "$deb has unexpected control member '$member' (maintainer scripts are not allowed)" ;;
		esac
	done

	echo "== linux/$arch .deb payload =="
	deb_listing=$(dpkg-deb --fsys-tarfile "$deb" | tar -tvf -)
	{
		for path in \
			usr/bin/routelatch \
			usr/share/doc/routelatch/README.md \
			usr/share/doc/routelatch/INSTALL.md \
			usr/share/doc/routelatch/SERVICE.md \
			usr/share/doc/routelatch/LICENSE \
			usr/share/doc/routelatch/NOTICE \
			usr/share/doc/routelatch/THIRD-PARTY-NOTICES.md \
			usr/share/doc/routelatch/docs/licensing.md \
			usr/share/doc/routelatch/go.sum \
			usr/share/doc/routelatch/BUILD-INFO \
			usr/share/doc/routelatch/PACKAGE-NOTES \
			usr/share/routelatch/systemd/routelatch.service \
			usr/lib/systemd/user/routelatch.service; do
			printf '%s\n' "$path"
		done
		if [ "$sidecar" = 1 ]; then
			printf '%s\n' usr/bin/sing-box usr/share/doc/routelatch/SINGBOX-INFO
		fi
	} | sort >"$tmp/deb.expected"
	printf '%s\n' "$deb_listing" | awk '{print $6}' |
		grep -v '/$' | grep -v '^\./$' | sed 's|^\./||' | sort >"$tmp/deb.actual"
	diff -u "$tmp/deb.expected" "$tmp/deb.actual" ||
		fail "$deb payload file set does not match the expected layout"

	printf '%s\n' "$deb_listing" |
		grep -Eq '^-rwxr-xr-x root/root +[0-9]+ .* \./usr/bin/routelatch$' ||
		fail "$deb /usr/bin/routelatch is not a 0755 root:root file"
	printf '%s\n' "$deb_listing" |
		grep -Eq '^-rw-r--r-- root/root +[0-9]+ .* \./usr/share/doc/routelatch/README.md$' ||
		fail "$deb /usr/share/doc/routelatch/README.md is not a 0644 root:root file"

	deb_bin_hash=$(dpkg-deb --fsys-tarfile "$deb" | tar -xO ./usr/bin/routelatch | sha256sum | awk '{print $1}')
	[ "$deb_bin_hash" = "$dist_bin_hash" ] ||
		fail "$deb /usr/bin/routelatch does not match $dist_bin"
	if dpkg-deb --fsys-tarfile "$deb" | tar -xO ./usr/share/routelatch/systemd/routelatch.service |
		cmp -s - "$repo/deploy/systemd/routelatch.service"; then
		:
	else
		fail "$deb share unit differs from deploy/systemd/routelatch.service"
	fi
	dpkg-deb --fsys-tarfile "$deb" | tar -xO ./usr/lib/systemd/user/routelatch.service |
		grep -q '^ExecStart=/usr/bin/routelatch run$' ||
		fail "$deb user unit does not start /usr/bin/routelatch"

	if [ "$sidecar" = 1 ]; then
		deb_sidecar_hash=$(dpkg-deb --fsys-tarfile "$deb" | tar -xO ./usr/bin/sing-box | sha256sum | awk '{print $1}')
		tar_sidecar_hash=$(tar -xOzf "$tar" "$name/bin/sing-box" | sha256sum | awk '{print $1}')
		[ "$deb_sidecar_hash" = "$tar_sidecar_hash" ] ||
			fail "$deb bin/sing-box does not match the tarball sidecar"
	fi

	echo "linux/$arch: tarball and .deb layout, metadata, binary and checksums match"
done

echo "PASS: package checksums, tarball layout and .deb metadata/payload are consistent"
