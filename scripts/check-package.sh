#!/bin/sh
# Verify the packages scripts/package.sh assembled: the exact checksum member
# set (tarballs, .deb files and RPM specs), tarball layout, .deb control
# metadata (including Debian architecture names), control archive members,
# payload file set, modes and the binary bytes in both containers. RPM specs
# are checked statically: Name/Version/Arch/License/Requires/Source0, the
# source digest pin, the %files install layout and the absence of scriptlets
# or service enablement. Packages must ship no systemd unit, autostart entry or
# maintainer script; `routelatch service install` writes the opt-in unit at
# runtime (docs/contracts.md §7.5). Binary RPMs are built and inspected by CI.
#
# It reads BUILD-INFO for the expected version and never installs anything.
# Usage: scripts/check-package.sh [dist/packages]
#        DIST=/path/to/dist scripts/check-package.sh
#        RELEASE_BUILD=1 scripts/check-package.sh   # release-package text check
#                                                     # (package.sh writes that
#                                                     # text only when
#                                                     # GITHUB_ACTIONS=true)
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
# Must match scripts/package.sh: strip a leading v, turn every hyphen into a
# dot (v0.1.0-r12.1 -> 0.1.0.r12.1) and prefix a value that would not start
# with a digit. The result is the exact Version of both .deb and RPM.
pkg_version=$(printf '%s' "$version" | sed -e 's/^v//' -e 's/-/./g')
case $pkg_version in
[0-9]*) ;;
*) pkg_version="0.0.0+$pkg_version" ;;
esac
case $pkg_version in
"" | *[!A-Za-z0-9.+~]*) fail "cannot form a package version from '$version'" ;;
esac
deb_version=$pkg_version
rpm_version=$pkg_version

# Must match scripts/package.sh: Debian calls little-endian 64-bit PowerPC
# "ppc64el" while Go calls it "ppc64le".
deb_arch_for() {
	case $1 in
	ppc64le) echo ppc64el ;;
	*) echo "$1" ;;
	esac
}

# Independent RPM mapping, deliberately written out rather than reused from
# package.sh so a wrong mapping in the producer is caught here.
rpm_arch_for_check() {
	case $1 in
	amd64) echo x86_64 ;;
	arm64) echo aarch64 ;;
	riscv64) echo riscv64 ;;
	ppc64le) echo ppc64le ;;
	*) fail "unexpected architecture '$1' (no RPM name known)" ;;
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
		"routelatch_${deb_version}_$(deb_arch_for "$arch").deb" \
		"routelatch-$(rpm_arch_for_check "$arch").spec" >>"$tmp/sums.expected"
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

# Static spec checks. The spec is the local RPM artifact; CI builds and
# inspects the binary RPM from it with rpmbuild/rpm.
expect_spec_line() {
	grep -Fxq "$1" "$spec" || fail "$spec is missing the line '$1'"
}

for arch in $arches; do
	name="routelatch-$version-linux-$arch"
	deb_arch=$(deb_arch_for "$arch")
	rpm_arch=$(rpm_arch_for_check "$arch")
	check_deb_arch "$deb_arch"
	tar="$stage/$name.tar.gz"
	deb="$stage/routelatch_${deb_version}_${deb_arch}.deb"
	spec="$stage/routelatch-$rpm_arch.spec"
	[ -f "$tar" ] || fail "missing $tar"
	[ -f "$deb" ] || fail "missing $deb (did package.sh run with dpkg-deb?)"
	[ -f "$spec" ] || fail "missing $spec (did package.sh run?)"
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
		bin/rlch \
		share/doc/routelatch/README.md \
		share/doc/routelatch/INSTALL.md \
		share/doc/routelatch/SERVICE.md \
		share/doc/routelatch/LICENSE \
		share/doc/routelatch/NOTICE \
		share/doc/routelatch/THIRD-PARTY-NOTICES.md \
		share/doc/routelatch/docs/licensing.md \
		share/doc/routelatch/go.sum \
		share/doc/routelatch/BUILD-INFO \
		share/doc/routelatch/PACKAGE-NOTES; do
		expect_file "$tar_files" "$name/$path" "$tar"
	done
	# A package built by the release workflow is published, so its notes must
	# not describe it as an unpublished local build (scripts/package.sh selects
	# the text with RELEASE_BUILD=1).
	if [ "${RELEASE_BUILD:-0}" = 1 ]; then
		notes=$(tar -xOzf "$tar" "$name/share/doc/routelatch/PACKAGE-NOTES")
		printf '%s\n' "$notes" | grep -q 'published with this version' ||
			fail "$tar PACKAGE-NOTES does not describe a published release"
		if printf '%s\n' "$notes" | grep -qi 'local build'; then
			fail "$tar PACKAGE-NOTES still describes a local build"
		fi
	fi
	# The package carries no install-time service or desktop integration: no
	# unit, no autostart entry and no systemd path.
	if printf '%s\n' "$tar_files" |
		grep -Eq '(\.service$|\.desktop$|(^|/)systemd(/|$)|(^|/)autostart/)'; then
		fail "$tar ships a systemd unit, autostart entry or systemd path"
	fi
	# tar -t output is "mode owner/group size date time path -> target"; require
	# the exact relative link, not a second copy of the binary.
	tar_link_mode=$(tar -tvzf "$tar" |
		awk -v path="$name/bin/rlch" '$NF == "routelatch" && $(NF-2) == path {print $1}')
	[ "$tar_link_mode" = lrwxrwxrwx ] ||
		fail "$tar bin/rlch is not a symlink to routelatch"
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
			usr/bin/rlch \
			usr/share/doc/routelatch/README.md \
			usr/share/doc/routelatch/INSTALL.md \
			usr/share/doc/routelatch/SERVICE.md \
			usr/share/doc/routelatch/LICENSE \
			usr/share/doc/routelatch/NOTICE \
			usr/share/doc/routelatch/THIRD-PARTY-NOTICES.md \
			usr/share/doc/routelatch/docs/licensing.md \
			usr/share/doc/routelatch/go.sum \
			usr/share/doc/routelatch/BUILD-INFO \
			usr/share/doc/routelatch/PACKAGE-NOTES; do
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
	if printf '%s\n' "$deb_listing" |
		grep -Eq '(\.service$|\.desktop$|/usr/lib/systemd/|/usr/share/routelatch/systemd/)'; then
		fail "$deb ships a systemd unit, autostart entry or systemd path"
	fi

	printf '%s\n' "$deb_listing" |
		grep -Eq '^-rwxr-xr-x root/root +[0-9]+ .* \./usr/bin/routelatch$' ||
		fail "$deb /usr/bin/routelatch is not a 0755 root:root file"
	deb_link_mode=$(printf '%s\n' "$deb_listing" |
		awk '$NF == "routelatch" && $(NF-2) == "./usr/bin/rlch" {print $1}')
	[ "$deb_link_mode" = lrwxrwxrwx ] ||
		fail "$deb /usr/bin/rlch is not a symlink to routelatch"
	printf '%s\n' "$deb_listing" |
		grep -Eq '^-rw-r--r-- root/root +[0-9]+ .* \./usr/share/doc/routelatch/README.md$' ||
		fail "$deb /usr/share/doc/routelatch/README.md is not a 0644 root:root file"

	deb_bin_hash=$(dpkg-deb --fsys-tarfile "$deb" | tar -xO ./usr/bin/routelatch | sha256sum | awk '{print $1}')
	[ "$deb_bin_hash" = "$dist_bin_hash" ] ||
		fail "$deb /usr/bin/routelatch does not match $dist_bin"

	if [ "$sidecar" = 1 ]; then
		deb_sidecar_hash=$(dpkg-deb --fsys-tarfile "$deb" | tar -xO ./usr/bin/sing-box | sha256sum | awk '{print $1}')
		tar_sidecar_hash=$(tar -xOzf "$tar" "$name/bin/sing-box" | sha256sum | awk '{print $1}')
		[ "$deb_sidecar_hash" = "$tar_sidecar_hash" ] ||
			fail "$deb bin/sing-box does not match the tarball sidecar"
	fi

	echo "== linux/$arch RPM spec ($rpm_arch) =="
	expect_spec_line "# CI builds the binary RPM with rpmbuild --target $rpm_arch from this spec."
	expect_spec_line "# BUILD-INFO version: $version"
	expect_spec_line "# BUILD-INFO commit: $(sed -n 's/^commit=//p' "$info")"
	expect_spec_line "# BUILD-INFO date: $(sed -n 's/^date=//p' "$info")"
	tar_hash=$(sha256sum "$tar" | awk '{print $1}')
	expect_spec_line "# Source0 SHA-256: $tar_hash"
	expect_spec_line "Name:           routelatch"
	expect_spec_line "Version:        $rpm_version"
	expect_spec_line "Release:        1"
	expect_spec_line "License:        Apache-2.0"
	expect_spec_line "URL:            https://github.com/m-hosseinpour/routelatch"
	expect_spec_line "Source0:        $name.tar.gz"
	# A foreign BuildArch makes rpmbuild fail with "No compatible architectures
	# found for build" on a different host; --target alone must set the arch.
	# RPM tags are case-insensitive and may be indented, so match the tag name
	# loosely and reject "BuildArch:", "buildarch=", "BUILDARCH :", etc.
	if grep -Eiq '^[[:space:]]*BuildArch[[:space:]]*([=:]|$)' "$spec"; then
		fail "$spec sets BuildArch, which prevents cross-architecture RPM builds"
	fi
	expect_spec_line "# No BuildArch: the arch comes from --target so foreign arches build on any host."
	expect_spec_line "AutoReqProv:    no"
	expect_spec_line "Requires:       ca-certificates"
	expect_spec_line "%global debug_package %{nil}"
	for section in %description %prep %build %install %files; do
		expect_spec_line "$section"
	done
	# The standard RPM layout puts the license texts in licenses/, every other
	# document in doc/, and a prebuilt static binary in /usr/bin.
	expect_spec_line 'install -D -m 0755 bin/routelatch %{buildroot}%{_bindir}/routelatch'
	expect_spec_line 'ln -s routelatch %{buildroot}%{_bindir}/rlch'
	expect_spec_line 'install -m 0644 share/doc/routelatch/LICENSE %{buildroot}%{_datadir}/licenses/routelatch/LICENSE'
	expect_spec_line 'install -m 0644 share/doc/routelatch/NOTICE %{buildroot}%{_datadir}/licenses/routelatch/NOTICE'
	expect_spec_line '%license %{_datadir}/licenses/routelatch/LICENSE'
	expect_spec_line '%license %{_datadir}/licenses/routelatch/NOTICE'
	for path in \
		%{_bindir}/routelatch \
		%{_bindir}/rlch \
		%{_datadir}/doc/routelatch/README.md \
		%{_datadir}/doc/routelatch/INSTALL.md \
		%{_datadir}/doc/routelatch/SERVICE.md \
		%{_datadir}/doc/routelatch/THIRD-PARTY-NOTICES.md \
		%{_datadir}/doc/routelatch/PACKAGE-NOTES \
		%{_datadir}/doc/routelatch/BUILD-INFO \
		%{_datadir}/doc/routelatch/go.sum \
		%{_datadir}/doc/routelatch/docs/licensing.md; do
		expect_spec_line "$path"
	done
	if grep -Fq '%{_datadir}/doc/routelatch/LICENSE' "$spec" ||
		grep -Fq '%{_datadir}/doc/routelatch/NOTICE' "$spec"; then
		fail "$spec puts LICENSE/NOTICE in the doc directory instead of licenses/"
	fi
	# No maintainer scriptlets and no service enable; the unit is data only.
	if grep -Eiq '^%(pre|post|preun|postun|pretrans|posttrans|triggerin|triggerun|triggerpostun|verifyscript|filetriggerin|filetriggerun|filetriggerpostun|transfiletriggerin|transfiletriggerun|transfiletriggerpostun)([[:space:]]|$)' "$spec"; then
		fail "$spec contains a maintainer scriptlet"
	fi
	if grep -Eiq 'systemctl|%systemd_|enable --now|chkconfig' "$spec"; then
		fail "$spec enables a service"
	fi
	# The unit/autostart entry are created only by `routelatch service install`
	# or `routelatch autostart enable`, never by the package.
	if grep -Eiq 'systemd|routelatch\.service|\.desktop' "$spec"; then
		fail "$spec ships a systemd unit or autostart entry"
	fi
	if [ "$sidecar" = 1 ]; then
		expect_spec_line 'install -D -m 0755 bin/sing-box %{buildroot}%{_bindir}/sing-box'
		expect_spec_line '%{_bindir}/sing-box'
		expect_spec_line '%{_datadir}/doc/routelatch/SINGBOX-INFO'
	else
		if grep -Fq 'sing-box' "$spec"; then
			fail "$spec has sidecar content although linux/$arch has no sidecar"
		fi
	fi

	echo "linux/$arch: tarball, .deb and RPM spec layout, metadata and checksums match"
done

echo "PASS: package checksums, tarball layout, .deb metadata/payload and RPM specs are consistent"
