#!/bin/sh
# Assemble local Linux packages from binaries that scripts/build.sh already
# produced. Nothing here is installed, enabled or published: the output is a
# directory tree and a .tar.gz under ./dist for inspection and for the user's
# own install steps (packaging/README.md).
#
# Usage: scripts/package.sh            (after scripts/build.sh)
set -eu

cd "$(dirname "$0")/.."
repo=$(pwd)
dist="$repo/dist"
info="$dist/BUILD-INFO"
stage="$dist/packages"

if [ ! -f "$info" ]; then
	echo "missing $info; run scripts/build.sh first" >&2
	exit 2
fi
version=$(sed -n 's/^version=//p' "$info")
epoch=$(sed -n 's/^source_date_epoch=//p' "$info")
if [ -z "$epoch" ]; then
	epoch=$(date -u -d "$(sed -n 's/^date=//p' "$info")" +%s)
fi

rm -rf "$stage"
mkdir -p "$stage"
: >"$stage/SHA256SUMS"

for arch in amd64 arm64; do
	name="routelatch-$version-linux-$arch"
	root="$stage/$name"
	bin="$dist/routelatch-linux-$arch"
	if [ ! -x "$bin" ]; then
		echo "missing $bin; run scripts/build.sh first" >&2
		exit 2
	fi
	mkdir -p "$root/bin" "$root/share/doc/routelatch/docs" "$root/share/routelatch/systemd"
	install -m 0755 "$bin" "$root/bin/routelatch"
	install -m 0644 "$info" "$root/share/doc/routelatch/BUILD-INFO"
	# The unit lives in deploy/ in the source tree; the package copies it
	# instead of keeping a second copy in packaging/.
	install -m 0644 "$repo/deploy/systemd/routelatch.service" "$root/share/routelatch/systemd/routelatch.service"
	install -m 0644 "$repo/README.md" "$root/share/doc/routelatch/README.md"
	install -m 0644 "$repo/packaging/README.md" "$root/share/doc/routelatch/INSTALL.md"
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
	# Deterministic tar: sorted names, fixed mtime/ownership, gzip without a
	# timestamp, so the same inputs produce the same archive.
	( cd "$stage" && tar --sort=name --mtime="@$epoch" --owner=0 --group=0 \
		--numeric-owner -cf - "$name" | gzip -n >"$stage/$name.tar.gz" )
	( cd "$stage" && sha256sum "$name.tar.gz" >>SHA256SUMS )
	echo "packaged $name.tar.gz"
done

echo
echo "package roots and tarballs are under $stage:"
find "$stage" -maxdepth 1 -name '*.tar.gz' -o -maxdepth 1 -name 'SHA256SUMS' | sort
echo
cat "$stage/SHA256SUMS"
echo
echo "inspect with: tar tzf dist/packages/routelatch-$version-linux-amd64.tar.gz"
