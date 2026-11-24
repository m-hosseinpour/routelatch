#!/bin/sh
# Verify that every built asset reached the draft release with the same name,
# size and sha256 digest the build produced.
#
# Usage: REPO=owner/name TAG=v1.2.3 scripts/release-verify-upload.sh <assets-dir>
#
# GitHub can report a freshly uploaded asset's .digest as null for a moment, so
# the release API is polled a bounded number of times. Any missing digest or
# size, a different asset set, or a mismatch fails closed; this script never
# publishes anything and never deletes a release.
#
# VERIFY_ATTEMPTS (default 12) and VERIFY_DELAY seconds (default 5) bound the
# polling. The assets directory must hold exactly the 17 release files.
set -eu

: "${REPO:?set REPO to owner/name}"
: "${TAG:?set TAG to the release tag}"
assets=${1:-}
[ -d "$assets" ] || {
	echo "usage: REPO=<owner/name> TAG=<tag> $0 <assets-dir>" >&2
	exit 2
}
attempts=${VERIFY_ATTEMPTS:-12}
delay=${VERIFY_DELAY:-5}

tmp=$(mktemp -d "${TMPDIR:-/tmp}/routelatch-verify.XXXXXX")
trap 'rm -rf "$tmp"' EXIT INT TERM
json="$tmp/release.json"

# The release must be a draft with exactly 17 assets and, for every one, a
# numeric size and a sha256: digest. Anything less is not yet verifiable.
complete() {
	jq -e '
		(.draft == true)
		and (((.assets // []) | length) == 17)
		and all(.assets[];
			((.size | type) == "number") and (.size > 0)
			and ((.digest | type) == "string")
			and (.digest | startswith("sha256:")))
	' "$1" >/dev/null 2>&1
}

attempt=0
while :; do
	attempt=$((attempt + 1))
	gh api "repos/$REPO/releases/tags/$TAG" >"$json" ||
		{ echo "cannot read the draft release for $TAG" >&2; exit 1; }
	complete "$json" && break
	if [ "$attempt" -ge "$attempts" ]; then
		jq -r '.assets[]?.name' "$json" | LC_ALL=C sort >"$tmp/api.names"
		for file in "$assets"/*; do
			basename "$file"
		done | LC_ALL=C sort >"$tmp/local.names"
		comm -23 "$tmp/local.names" "$tmp/api.names" |
			sed 's/^/built file missing from the release: /' >&2
		comm -13 "$tmp/local.names" "$tmp/api.names" |
			sed 's/^/release asset not among the built files: /' >&2
		jq -r '.assets[]?
			| select(((.digest | type) != "string")
				or ((.size | type) != "number") or (.size <= 0))
			| "asset without a complete digest/size: \(.name)"' "$json" >&2 || true
		echo "the release API reports $(jq -r '(.assets // []) | length' "$json")" \
			"assets, expected $(wc -l <"$tmp/local.names" | tr -d ' '), and did" \
			"not report the draft, the exact asset set and every sha256 digest" \
			"after $attempt checks; not publishing" >&2
		exit 1
	fi
	sleep "$delay"
done

[ "$(jq -r '.tag_name' "$json")" = "$TAG" ] ||
	{ echo "the draft release has the wrong tag"; exit 1; }
jq -r '.assets[] | "\(.name)\t\(.size)\t\(.digest)"' "$json" |
	LC_ALL=C sort >"$tmp/assets.api"
for file in "$assets"/*; do
	printf '%s\t%s\tsha256:%s\n' "$(basename "$file")" "$(stat -c '%s' "$file")" \
		"$(sha256sum "$file" | awk '{print $1}')"
done | LC_ALL=C sort >"$tmp/assets.local"
diff -u "$tmp/assets.local" "$tmp/assets.api" ||
	{ echo "uploaded asset name/size/digest differs from the built files"; exit 1; }
echo "all 17 uploaded assets match the built name, size and sha256 digest"
