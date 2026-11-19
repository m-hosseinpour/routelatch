#!/bin/sh
# Assemble local Linux packages from binaries that scripts/build.sh already
# produced. Nothing here is installed, enabled or published: the output is a
# directory tree, a .tar.gz and a .deb under ./dist for inspection and for the
# user's own install steps (packaging/README.md).
#
# Usage: scripts/package.sh            (after scripts/build.sh)
#
# DIST overrides the build/output directory (default ./dist). REQUIRE_DEB=1
# makes a missing dpkg-deb a hard error; otherwise the .deb step is skipped
# with a notice. REQUIRE_SINGBOX=1 keeps the existing release-build gate.
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

# Debian version: strip a leading "v" and make sure the upstream version starts
# with a digit. A git-describe value such as "a028469" is not a valid Debian
# version, so it is prefixed rather than silently rejected. The Debian
# character set is checked here so dpkg-deb never sees an invalid value.
deb_version=$(printf '%s' "$version" | sed 's/^v//')
case $deb_version in
[0-9]*) ;;
*) deb_version="0.0.0+$deb_version" ;;
esac
case $deb_version in
"" | *[!A-Za-z0-9.+~-]*)
	echo "cannot form a Debian version from BUILD-INFO version '$version'" >&2
	exit 2
	;;
esac
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
	mkdir -p "$root/bin" "$root/share/doc/routelatch/docs" "$root/share/routelatch/systemd"
	install -m 0755 "$bin" "$root/bin/routelatch"
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
	# The unit lives in deploy/ in the source tree; the package copies it
	# instead of keeping a second copy in packaging/.
	install -m 0644 "$repo/deploy/systemd/routelatch.service" "$root/share/routelatch/systemd/routelatch.service"
	install -m 0644 "$repo/README.md" "$root/share/doc/routelatch/README.md"
	install -m 0644 "$repo/packaging/README.md" "$root/share/doc/routelatch/INSTALL.md"
	# INSTALL.md sends readers to deploy/README.md for uninstall and crash
	# recovery, so that document ships too. Its paths describe the source tree;
	# this header maps them onto the extracted package.
	{
		cat <<'SERVICE_HEADER'
This is deploy/README.md from the RouteLatch source tree, shipped as SERVICE.md
so the install notes resolve inside this package. Paths written as
deploy/systemd/... and references such as docs/contracts.md §7.1 refer to the
source tree, not to this package. The unit in this package is
share/routelatch/systemd/routelatch.service, and INSTALL.md has the install
commands that work from the extracted package. The readiness checks that run
tests and gated systemd tests describe the source tree, not this package.
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
system-wide install.
EOF
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
		# lookup still finds it. A standard user unit is added because the
		# tarball unit's %h/.local/bin path is not an installed package path.
		deb_arch=$(deb_arch_for "$arch")
		deb="$stage/routelatch_${deb_version}_${deb_arch}.deb"
		deb_root="$stage/.deb-$name"
		rm -rf "$deb_root"
		mkdir -p "$deb_root/usr"
		cp -a "$root/bin" "$root/share" "$deb_root/usr/"
		unit="$deb_root/usr/lib/systemd/user/routelatch.service"
		mkdir -p "$(dirname "$unit")"
		sed 's|^ExecStart=%h/.local/bin/routelatch run$|ExecStart=/usr/bin/routelatch run|' \
			"$repo/deploy/systemd/routelatch.service" >"$unit"
		chmod 0644 "$unit"
		if ! grep -q '^ExecStart=/usr/bin/routelatch run$' "$unit"; then
			echo "cannot adapt the systemd unit for the .deb; deploy/systemd/routelatch.service changed" >&2
			exit 1
		fi
		# Measure the installed payload, including the generated unit, so the
		# estimate is not taken from the slightly smaller tarball tree. Sum file
		# bytes rather than disk blocks so the value does not depend on the
		# builder's filesystem block size.
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
done

echo
echo "package roots, tarballs and .deb files are under $stage:"
find "$stage" -maxdepth 1 \( -name '*.tar.gz' -o -name '*.deb' -o -name 'SHA256SUMS' \) | sort
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
for arch in $arches; do
	echo "inspect with: tar tzf $stage/routelatch-$version-linux-$arch.tar.gz"
	break
done
