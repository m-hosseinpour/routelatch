#!/bin/sh
# Assemble local Linux packages from binaries that scripts/build.sh already
# produced. Nothing here is installed, enabled or published: the output is a
# directory tree, a .tar.gz, a .deb and an RPM .spec under ./dist for inspection
# and for the user's own install steps (packaging/README.md). Packages ship no
# systemd unit and no maintainer scripts; the opt-in user unit is written only
# by `routelatch service install` (docs/contracts.md §7.5).
#
# Usage: scripts/package.sh            (after scripts/build.sh)
#
# DIST overrides the build/output directory (default ./dist). REQUIRE_DEB=1
# makes a missing dpkg-deb a hard error; otherwise the .deb step is skipped
# with a notice. REQUIRE_SINGBOX=1 keeps the existing release-build gate.
# Binary RPMs are not built here: rpmbuild is not assumed to exist locally. One
# .spec per architecture is generated and checksummed instead; CI builds and
# inspects the binary RPMs from those specs (see .github/workflows/ci.yml).
set -eu
# Directory modes come from mkdir's umask; pin it so the tarball does not change
# with the builder's shell settings (a 002 umask produced group-writable dirs).
umask 022

cd "$(dirname "$0")/.."
repo=$(pwd)
dist=${DIST:-$repo/dist}
info="$dist/BUILD-INFO"
stage="$dist/packages"
arches=${ARCHES:-"amd64 arm64 riscv64 ppc64le"}
# sing-box ships official release binaries for these architectures only. The
# project pins their upstream archive digests; it does not invent pins for
# architectures upstream does not provide.
singbox_arches="amd64 arm64"
# Debian architecture names are not always Go's GOARCH: little-endian 64-bit
# PowerPC is "ppc64el" in Debian and "ppc64le" in Go. The .deb filename and its
# Architecture field use the Debian name; tarballs keep the Go/CPU name.
deb_arch_for() {
	case $1 in
	ppc64le) echo ppc64el ;;
	*) echo "$1" ;;
	esac
}
# RPM architecture names differ from Go's GOARCH for the two architectures
# below; ppc64le and riscv64 keep their Go names. The CI rpmbuild --target uses
# this value. The spec deliberately has no BuildArch: rpmbuild rejects a foreign
# BuildArch on a different host ("No compatible architectures found for build"),
# while --target alone tags the binary RPM with the requested architecture.
rpm_arch_for() {
	case $1 in
	amd64) echo x86_64 ;;
	arm64) echo aarch64 ;;
	*) echo "$1" ;;
	esac
}

if [ ! -f "$info" ]; then
	echo "missing $info; run scripts/build.sh first" >&2
	exit 2
fi
version=$(sed -n 's/^version=//p' "$info")
case $(uname -m) in
x86_64) host_arch=amd64 ;;
aarch64 | arm64) host_arch=arm64 ;;
riscv64) host_arch=riscv64 ;;
ppc64le) host_arch=ppc64le ;;
*) host_arch=unknown ;;
esac
if [ "$host_arch" = unknown ]; then
	echo "warning: unknown host architecture; package stamp will not be verified" >&2
fi
epoch=$(sed -n 's/^source_date_epoch=//p' "$info")
if [ -z "$epoch" ]; then
	epoch=$(date -u -d "$(sed -n 's/^date=//p' "$info")" +%s)
fi

# One normalised version for every package format. Debian and RPM both reject a
# hyphen in this position, so strip a leading "v" and turn every "-" into a "."
# (v0.1.0-r12.1 -> 0.1.0.r12.1). A git-describe value such as "a028469" does
# not start with a digit, so it is prefixed instead of being silently rejected.
# The character set is the intersection of Debian's and RPM's version alphabets;
# checking it here keeps dpkg-deb and rpmbuild from seeing an invalid value.
# scripts/check-package.sh must apply the same normalization.
pkg_version=$(printf '%s' "$version" | sed -e 's/^v//' -e 's/-/./g')
case $pkg_version in
[0-9]*) ;;
*) pkg_version="0.0.0+$pkg_version" ;;
esac
case $pkg_version in
"" | *[!A-Za-z0-9.+~]*)
	echo "cannot form a package version from BUILD-INFO version '$version'" >&2
	exit 2
	;;
esac
# Keep the Phase 1 names as aliases so the .deb block below stays readable; both
# formats use the exact same string as their Version field.
deb_version=$pkg_version
rpm_version=$pkg_version
deb_maintainer=${DEB_MAINTAINER:-"Mahdi Hosseinpour <mahdihosseinpour1996@gmail.com>"}
build_deb=0
if command -v dpkg-deb >/dev/null 2>&1; then
	build_deb=1
elif [ "${REQUIRE_DEB:-0}" = 1 ]; then
	echo "REQUIRE_DEB=1 but dpkg-deb is not installed" >&2
	exit 2
else
	echo "note: dpkg-deb not found; skipping .deb packages (set REQUIRE_DEB=1 to require them)" >&2
fi

# Optional, SEPARATE GPL-3.0 executable (R11). sing-box is not built here and is
# not linked into RouteLatch; it is an already extracted, unmodified upstream
# binary per architecture, supplied under dist/singbox/ by the release operator
# together with a SHA256SUMS generated after extraction. packaging/README.md
# documents how to obtain and verify those inputs from the official v1.14.4
# release; this script never downloads anything. The tag/commit are pinned so
# every package records exactly which upstream release the sidecar came from.
SINGBOX_TAG=v1.14.4
SINGBOX_COMMIT=c005f35300ce76427dc285481ec5695f5b179cd2
SINGBOX_BIN_DIR=${SINGBOX_BIN_DIR:-$dist/singbox}
SINGBOX_SUMS="$SINGBOX_BIN_DIR/SHA256SUMS"

# Authoritative per-architecture SHA-256 of the OFFICIAL RELEASE ARCHIVES (not of
# the extracted binaries). These are the digests GitHub reports for the v1.14.4
# release assets (release id 421998786, published 2026-10-24, tag v1.14.4); that
# release has no separate SHA256SUMS asset. Upstream publishes no per-binary
# digest, so the operator must extract from an archive verified against these
# values. When an official archive is supplied it is re-verified here before its
# extracted binary is accepted; REQUIRE_SINGBOX=1 makes the archive mandatory.
SINGBOX_ARCHIVE_AMD64=sing-box-1.14.4-linux-amd64.tar.gz
SINGBOX_ARCHIVE_AMD64_SHA256=699a1b510aa4396b2176d32d1181920399ebf1e267835da6f3b598d0f7c24968
SINGBOX_ARCHIVE_ARM64=sing-box-1.14.4-linux-arm64.tar.gz
SINGBOX_ARCHIVE_ARM64_SHA256=436441f76ee2f7fe21b5b2d426d3cb261a480455a704865789ad533e4273ec26

singbox_present=0
if [ -d "$SINGBOX_BIN_DIR" ]; then
	if [ ! -f "$SINGBOX_SUMS" ]; then
		if [ "${REQUIRE_SINGBOX:-0}" = 1 ]; then
			echo "REQUIRE_SINGBOX=1 but $SINGBOX_SUMS is missing; see packaging/README.md ('The sing-box sidecar')" >&2
			exit 2
		fi
		echo "note: $SINGBOX_SUMS is missing; skipping the sing-box sidecar for this routine build (set REQUIRE_SINGBOX=1 for release builds)" >&2
	else
		singbox_present=1
	fi
else
	if [ "${REQUIRE_SINGBOX:-0}" = 1 ]; then
		echo "REQUIRE_SINGBOX=1 but no sidecar directory $SINGBOX_BIN_DIR" >&2
		exit 2
	fi
	echo "note: no sing-box sidecar in $SINGBOX_BIN_DIR; packaging without it (set REQUIRE_SINGBOX=1 for release builds)"
fi

rm -rf "$stage"
mkdir -p "$stage"
: >"$stage/SHA256SUMS"

for arch in $arches; do
	name="routelatch-$version-linux-$arch"
	root="$stage/$name"
	bin="$dist/routelatch-linux-$arch"
	if [ ! -x "$bin" ]; then
		echo "missing $bin; run scripts/build.sh first" >&2
		exit 2
	fi
	mkdir -p "$root/bin" "$root/share/doc/routelatch/docs"
	install -m 0755 "$bin" "$root/bin/routelatch"
	# The short command is a relative symlink so the tarball works under any
	# prefix and the package does not carry a second copy of the binary.
	ln -s routelatch "$root/bin/rlch"
	# A package must never ship a binary whose stamp disagrees with BUILD-INFO.
	# Only the host architecture can run here; build.sh already proved the other
	# architecture embeds the same strings. Compare all three stamped values, not
	# just the version string.
	if [ "$arch" = "$host_arch" ]; then
		if ! got=$("$root/bin/routelatch" version); then
			echo "package stamp check FAILED: packaged binary did not run" >&2
			exit 1
		fi
		got_version=$(printf '%s\n' "$got" | sed -n 's/^RouteLatch //p')
		got_commit=$(printf '%s\n' "$got" | sed -n 's/^commit: //p')
		got_date=$(printf '%s\n' "$got" | sed -n 's/^build date: //p')
		want_commit=$(sed -n 's/^commit=//p' "$info")
		want_date=$(sed -n 's/^date=//p' "$info")
		if [ "$got_version" != "$version" ] ||
			[ "$got_commit" != "$want_commit" ] ||
			[ "$got_date" != "$want_date" ]; then
			echo "package stamp check FAILED: binary reports version '$got_version', commit '$got_commit', date '$got_date'; BUILD-INFO says '$version', '$want_commit', '$want_date'" >&2
			exit 1
		fi
		echo "package stamp $arch: version, commit and date match BUILD-INFO"
	fi

	# Verify the supplied sidecar before it enters the package. SHA256SUMS is
	# generated by the operator after extraction; re-checking it here makes a
	# wrong or tampered binary fail closed. The source archive, when supplied, is
	# additionally checked against the authoritative upstream digest pinned above.
	sidecar_arch=0
	case " $singbox_arches " in
	*" $arch "*) sidecar_arch=1 ;;
	esac
	if [ "$singbox_present" = 1 ] && [ "$sidecar_arch" = 0 ]; then
		echo "note: no pinned sing-box sidecar exists for linux/$arch; packaging without it" >&2
	fi
	if [ "$singbox_present" = 1 ] && [ "$sidecar_arch" = 1 ]; then
		sb_src="$SINGBOX_BIN_DIR/sing-box-linux-$arch"
		[ -f "$sb_src" ] || {
			echo "missing $sb_src for pinned sing-box $SINGBOX_TAG" >&2
			exit 2
		}
		# Anchor provenance to the authoritative upstream digest when the official
		# archive is present. GitHub publishes no per-binary digest, so this is the
		# strongest public pin available; the operator must extract from the archive
		# that passed this check (packaging/README.md).
		if [ "$arch" = amd64 ]; then
			sb_archive=$SINGBOX_ARCHIVE_AMD64
			sb_archive_hash=$SINGBOX_ARCHIVE_AMD64_SHA256
		else
			sb_archive=$SINGBOX_ARCHIVE_ARM64
			sb_archive_hash=$SINGBOX_ARCHIVE_ARM64_SHA256
		fi
		sb_archive_path="$SINGBOX_BIN_DIR/$sb_archive"
		if [ -f "$sb_archive_path" ]; then
			sb_archive_got=$(sha256sum "$sb_archive_path" | awk '{print $1}')
			if [ "$sb_archive_got" != "$sb_archive_hash" ]; then
				echo "official archive checksum FAILED for linux/$arch: $sb_archive is $sb_archive_got, expected $sb_archive_hash" >&2
				exit 1
			fi
			echo "sing-box official archive linux/$arch: sha256 $sb_archive_got matches pinned v1.14.4 digest"
		elif [ "${REQUIRE_SINGBOX:-0}" = 1 ]; then
			echo "REQUIRE_SINGBOX=1 but $sb_archive_path is missing; cannot anchor provenance for linux/$arch" >&2
			exit 2
		else
			echo "note: no $sb_archive_path; sing-box provenance for linux/$arch is not anchored to an upstream digest (routine build only)"
		fi
		sb_line=$(grep -E "[[:space:]]sing-box-linux-$arch\$" "$SINGBOX_SUMS" || true)
		if [ -z "$sb_line" ]; then
			echo "no SHA256SUMS entry for sing-box-linux-$arch in $SINGBOX_SUMS" >&2
			exit 2
		fi
		if ! printf '%s\n' "$sb_line" | ( cd "$SINGBOX_BIN_DIR" && sha256sum -c - ); then
			echo "sing-box checksum check FAILED for linux/$arch; see packaging/README.md" >&2
			exit 1
		fi
		sb_hash=$(sha256sum "$sb_src" | awk '{print $1}')
		install -m 0755 "$sb_src" "$root/bin/sing-box"
		{
			printf '# Separate GPL-3.0 executable, NOT part of RouteLatch\n'
			printf 'sing-box tag: %s\n' "$SINGBOX_TAG"
			printf 'sing-box commit: %s\n' "$SINGBOX_COMMIT"
			printf 'binary: bin/sing-box in the tarball, /usr/bin/sing-box in the .deb (linux/%s)\n' "$arch"
			printf 'sha256: %s\n' "$sb_hash"
			printf 'source offer: see THIRD-PARTY-NOTICES.md and docs/licensing.md\n'
		} >"$root/share/doc/routelatch/SINGBOX-INFO"
		chmod 0644 "$root/share/doc/routelatch/SINGBOX-INFO"
		echo "sing-box linux/$arch: sha256 $sb_hash (tag $SINGBOX_TAG) verified"
	fi

	# BUILD-INFO stays the RouteLatch build stamp and is copied unchanged; the
	# sidecar is recorded in SINGBOX-INFO so the stamp check above and this file
	# remain consistent with the binary built by scripts/build.sh.
	install -m 0644 "$info" "$root/share/doc/routelatch/BUILD-INFO"
	install -m 0644 "$repo/README.md" "$root/share/doc/routelatch/README.md"
	install -m 0644 "$repo/packaging/README.md" "$root/share/doc/routelatch/INSTALL.md"
	# INSTALL.md sends readers to deploy/README.md for uninstall and crash
	# recovery, so that document ships too. Its paths describe the source tree;
	# this header maps them onto the extracted package.
	{
		cat <<'SERVICE_HEADER'
This is deploy/README.md from the RouteLatch source tree, shipped as SERVICE.md
so the install notes resolve inside this package. References such as
docs/contracts.md §7.5 and paths written as deploy/... name the source tree,
not this package. The package ships no systemd unit and no maintainer scripts:
the only way to create the opt-in user unit is `routelatch service install`
(see INSTALL.md). Test and gated-check instructions describe the source tree.
SERVICE_HEADER
		cat "$repo/deploy/README.md"
	} >"$root/share/doc/routelatch/SERVICE.md"
	chmod 0644 "$root/share/doc/routelatch/SERVICE.md"
	# Apache-2.0 requires the license and the project notice to travel with the
	# binary; THIRD-PARTY-NOTICES carries the dependency licenses and the MPL-2.0
	# source-availability statement.
	install -m 0644 "$repo/LICENSE" "$root/share/doc/routelatch/LICENSE"
	install -m 0644 "$repo/NOTICE" "$root/share/doc/routelatch/NOTICE"
	install -m 0644 "$repo/THIRD-PARTY-NOTICES.md" "$root/share/doc/routelatch/THIRD-PARTY-NOTICES.md"
	# The audit record is referenced by NOTICE and the notices; keep its source
	# path so those links resolve in the package. go.sum backs the module
	# hashes quoted in the audit.
	install -m 0644 "$repo/docs/licensing.md" "$root/share/doc/routelatch/docs/licensing.md"
	install -m 0644 "$repo/go.sum" "$root/share/doc/routelatch/go.sum"
	# RELEASE_BUILD=1 is set by the GitHub Actions release workflow: the same
	# package is built there and published with the release, so it must not
	# claim to be an unpublished local build. Local builds keep the default.
	if [ "${RELEASE_BUILD:-0}" = 1 ]; then
		cat >"$root/share/doc/routelatch/PACKAGE-NOTES" <<EOF
This package was built by the RouteLatch release workflow in GitHub Actions from
BUILD-INFO (version/commit/date above) and is published with this version's
GitHub Release, together with SHA256SUMS and the binary RPMs. It is reproducible
for the same inputs.

RouteLatch is licensed under the Apache License, Version 2.0; see LICENSE,
NOTICE and THIRD-PARTY-NOTICES.md in this directory. Verify the download with
the release's SHA256SUMS before installing it.

Installing is the user's action. This package was built without enabling a
service, without touching desktop proxy or network settings, and without a
system-wide install. It ships no systemd unit, autostart entry or maintainer
script: plain 'routelatch run' starts the detached app/tray, and creating the
opt-in user unit or login autostart is an explicit 'routelatch service install'
or 'routelatch autostart enable'.
EOF
	else
		cat >"$root/share/doc/routelatch/PACKAGE-NOTES" <<EOF
This is a local build assembled by scripts/package.sh from BUILD-INFO
(version/commit/date above). It is reproducible for the same inputs.

RouteLatch is licensed under the Apache License, Version 2.0; see LICENSE,
NOTICE and THIRD-PARTY-NOTICES.md in this directory. Assembling this package
does not publish anything: publishing a release, uploading a package or
distributing the source still needs a separate decision and the project's
release checklist.

Installing is the user's action. This package was built without enabling a
service, without touching desktop proxy or network settings, and without a
system-wide install. It ships no systemd unit, autostart entry or maintainer
script: plain 'routelatch run' starts the detached app/tray, and creating the
opt-in user unit or login autostart is an explicit 'routelatch service install'
or 'routelatch autostart enable'.
EOF
	fi
	if [ "$singbox_present" = 1 ] && [ "$sidecar_arch" = 1 ]; then
		cat >>"$root/share/doc/routelatch/PACKAGE-NOTES" <<EOF

This package also ships bin/sing-box (bin/sing-box in the tarball layout,
/usr/bin/sing-box in the .deb), a separate, unmodified GPL-3.0-or-later
executable (pinned tag $SINGBOX_TAG) that is NOT linked into RouteLatch and does
not change RouteLatch's Apache-2.0 license. Its SHA-256 is in SINGBOX-INFO; the
GPL-3.0 text, naming-clause note and written source offer are in
THIRD-PARTY-NOTICES.md, and the assessment is in docs/licensing.md. Installing
the tarball does not install or enable sing-box as a service.
EOF
	fi
	# Deterministic tar: sorted names, fixed mtime/ownership, gzip without a
	# timestamp, so the same inputs produce the same archive.
	( cd "$stage" && tar --sort=name --mtime="@$epoch" --owner=0 --group=0 \
		--numeric-owner -cf - "$name" | gzip -n >"$stage/$name.tar.gz" )
	( cd "$stage" && sha256sum "$name.tar.gz" >>SHA256SUMS )
	echo "packaged $name.tar.gz"

	if [ "$build_deb" = 1 ]; then
		# The tarball root maps onto /usr in the .deb: bin/routelatch becomes
		# /usr/bin/routelatch, share/doc/routelatch stays under /usr/share/doc,
		# and the sidecar stays next to the main binary so the runtime's sibling
		# lookup still finds it. No systemd unit or maintainer script is added;
		# `routelatch service install` writes the opt-in unit at runtime.
		deb_arch=$(deb_arch_for "$arch")
		deb="$stage/routelatch_${deb_version}_${deb_arch}.deb"
		deb_root="$stage/.deb-$name"
		rm -rf "$deb_root"
		mkdir -p "$deb_root/usr"
		cp -a "$root/bin" "$root/share" "$deb_root/usr/"
		# Measure the installed payload so the estimate is not taken from the
		# slightly different tarball tree. Sum file bytes rather than disk
		# blocks so the value does not depend on the builder's filesystem block
		# size.
		installed_size=$(find "$deb_root/usr" -type f -printf '%s\n' |
			awk '{ total += $1 } END { printf "%d\n", (total + 1023) / 1024 }')
		mkdir -p "$deb_root/DEBIAN"
		cat >"$deb_root/DEBIAN/control" <<EOF
Package: routelatch
Version: $deb_version
Architecture: $deb_arch
Maintainer: $deb_maintainer
Installed-Size: $installed_size
Depends: ca-certificates
Section: net
Priority: optional
Homepage: https://github.com/m-hosseinpour/routelatch
Description: targeted proxy client for Linux
 RouteLatch sends only approved destinations through a pool of proxy exits
 and keeps everything else direct. It embeds the pinned Xray core in one
 static binary and optionally ships the separate sing-box runtime.
EOF
		chmod 0644 "$deb_root/DEBIAN/control"
		# dpkg-deb takes data.tar mtimes from the filesystem and honors
		# SOURCE_DATE_EPOCH for the archive members, so both are pinned.
		find "$deb_root" -exec touch -h -d "@$epoch" {} +
		SOURCE_DATE_EPOCH=$epoch dpkg-deb --build --root-owner-group -Zgzip -z9 \
			"$deb_root" "$deb" >/dev/null
		rm -rf "$deb_root"
		( cd "$stage" && sha256sum "$(basename "$deb")" >>SHA256SUMS )
		echo "packaged $(basename "$deb")"
	fi

	# RPM: generate one spec per architecture from the same BUILD-INFO stamp and
	# the tarball we just wrote. rpmbuild is not assumed to exist on a local
	# host, so no binary .rpm is produced here; CI builds and inspects the RPMs
	# from these specs. The spec pins the tarball by SHA-256 so a wrong source
	# cannot silently become an RPM. LICENSE/NOTICE use the standard RPM
	# /usr/share/licenses/routelatch location; all other docs stay under
	# /usr/share/doc/routelatch. There are no scriptlets, no packaged systemd
	# unit and no service enable.
	rpm_arch=$(rpm_arch_for "$arch")
	spec="$stage/routelatch-$rpm_arch.spec"
	tar_hash=$(sha256sum "$stage/$name.tar.gz" | awk '{print $1}')
	spec_commit=$(sed -n 's/^commit=//p' "$info")
	spec_date=$(sed -n 's/^date=//p' "$info")
	with_sidecar=0
	if [ "$singbox_present" = 1 ] && [ "$sidecar_arch" = 1 ]; then
		with_sidecar=1
	fi
	{
		cat <<EOF
# Generated by scripts/package.sh from dist/BUILD-INFO. Do not edit by hand.
# BUILD-INFO version: $version
# BUILD-INFO commit: $spec_commit
# BUILD-INFO date: $spec_date
# Source0 SHA-256: $tar_hash
EOF
		if [ "${RELEASE_BUILD:-0}" = 1 ]; then
			cat <<EOF
# Unsigned CI build: SHA256SUMS is the only trust anchor. No GPG signing, no
# package repository, no packaged unit, no service enable and no maintainer
# scriptlets.
EOF
		else
			cat <<EOF
# Local, unsigned build: SHA256SUMS is the only trust anchor. No GPG signing,
# no package repository, no packaged unit, no service enable and no maintainer
# scriptlets.
EOF
		fi
		cat <<EOF
# CI builds the binary RPM with rpmbuild --target $rpm_arch from this spec.
# No BuildArch: the arch comes from --target so foreign arches build on any host.
Name:           routelatch
Version:        $rpm_version
Release:        1
Summary:        Targeted proxy client for Linux
License:        Apache-2.0
URL:            https://github.com/m-hosseinpour/routelatch
Source0:        $name.tar.gz
AutoReqProv:    no
Requires:       ca-certificates
%global debug_package %{nil}

%description
RouteLatch sends only approved destinations through a pool of proxy exits and
keeps everything else direct. It embeds the pinned Xray core in one static
binary. It is licensed under Apache-2.0.
EOF
		if [ "$with_sidecar" = 1 ]; then
			cat <<'EOF'
This build also ships bin/sing-box, a separate, unmodified GPL-3.0-or-later
executable used for hysteria2/sing-box exits. It is not linked into RouteLatch
and does not change its Apache-2.0 license.
EOF
		fi
		cat <<EOF

%prep
%setup -q -n $name

%build
# Prebuilt static binary: no compilation happens in the RPM build.

%install
rm -rf %{buildroot}
install -D -m 0755 bin/routelatch %{buildroot}%{_bindir}/routelatch
ln -s routelatch %{buildroot}%{_bindir}/rlch
install -d -m 0755 %{buildroot}%{_datadir}/licenses/routelatch
install -m 0644 share/doc/routelatch/LICENSE %{buildroot}%{_datadir}/licenses/routelatch/LICENSE
install -m 0644 share/doc/routelatch/NOTICE %{buildroot}%{_datadir}/licenses/routelatch/NOTICE
install -d -m 0755 %{buildroot}%{_datadir}/doc/routelatch
install -m 0644 share/doc/routelatch/README.md %{buildroot}%{_datadir}/doc/routelatch/README.md
install -m 0644 share/doc/routelatch/INSTALL.md %{buildroot}%{_datadir}/doc/routelatch/INSTALL.md
install -m 0644 share/doc/routelatch/SERVICE.md %{buildroot}%{_datadir}/doc/routelatch/SERVICE.md
install -m 0644 share/doc/routelatch/THIRD-PARTY-NOTICES.md %{buildroot}%{_datadir}/doc/routelatch/THIRD-PARTY-NOTICES.md
install -m 0644 share/doc/routelatch/PACKAGE-NOTES %{buildroot}%{_datadir}/doc/routelatch/PACKAGE-NOTES
install -m 0644 share/doc/routelatch/BUILD-INFO %{buildroot}%{_datadir}/doc/routelatch/BUILD-INFO
install -m 0644 share/doc/routelatch/go.sum %{buildroot}%{_datadir}/doc/routelatch/go.sum
install -d -m 0755 %{buildroot}%{_datadir}/doc/routelatch/docs
install -m 0644 share/doc/routelatch/docs/licensing.md %{buildroot}%{_datadir}/doc/routelatch/docs/licensing.md
EOF
		if [ "$with_sidecar" = 1 ]; then
			cat <<'EOF'
install -D -m 0755 bin/sing-box %{buildroot}%{_bindir}/sing-box
install -m 0644 share/doc/routelatch/SINGBOX-INFO %{buildroot}%{_datadir}/doc/routelatch/SINGBOX-INFO
EOF
		fi
		cat <<'EOF'

%files
%license %{_datadir}/licenses/routelatch/LICENSE
%license %{_datadir}/licenses/routelatch/NOTICE
%{_bindir}/routelatch
%{_bindir}/rlch
%{_datadir}/doc/routelatch/README.md
%{_datadir}/doc/routelatch/INSTALL.md
%{_datadir}/doc/routelatch/SERVICE.md
%{_datadir}/doc/routelatch/THIRD-PARTY-NOTICES.md
%{_datadir}/doc/routelatch/PACKAGE-NOTES
%{_datadir}/doc/routelatch/BUILD-INFO
%{_datadir}/doc/routelatch/go.sum
%{_datadir}/doc/routelatch/docs/licensing.md
EOF
		if [ "$with_sidecar" = 1 ]; then
			cat <<'EOF'
%{_bindir}/sing-box
%{_datadir}/doc/routelatch/SINGBOX-INFO
EOF
		fi
	} >"$spec"
	chmod 0644 "$spec"
	( cd "$stage" && sha256sum "$(basename "$spec")" >>SHA256SUMS )
	echo "generated $(basename "$spec") (binary RPM built and inspected in CI)"
done

echo
echo "package roots, tarballs, .deb files and RPM specs are under $stage:"
find "$stage" -maxdepth 1 \( -name '*.tar.gz' -o -name '*.deb' -o -name '*.spec' -o -name 'SHA256SUMS' \) | sort
echo
cat "$stage/SHA256SUMS"
echo
if [ "$singbox_present" = 1 ]; then
	echo "sing-box sidecar: included for $singbox_arches (tag $SINGBOX_TAG), verified against $SINGBOX_SUMS"
else
	echo "sing-box sidecar: not included (no usable sidecar inputs in $SINGBOX_BIN_DIR)"
fi
if [ "$build_deb" = 1 ]; then
	echo "Debian packages: routelatch_${deb_version}_<debian-arch>.deb (unsigned; verify with SHA256SUMS)"
	echo "Debian architecture names match GOARCH except ppc64le -> ppc64el."
fi
echo "RPM specs: routelatch-<rpm-arch>.spec (Version $rpm_version); binary RPMs are built and inspected by CI."
echo "RPM architecture names: amd64->x86_64, arm64->aarch64, riscv64 and ppc64le unchanged."
for arch in $arches; do
	echo "inspect with: tar tzf $stage/routelatch-$version-linux-$arch.tar.gz"
	break
done
