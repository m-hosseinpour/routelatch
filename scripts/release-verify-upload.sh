#!/bin/sh
# Verify that every built asset reached the draft release with the same name,
# size and sha256 digest the build produced.
#
# Usage:
#   REPO=owner/name TAG=v1.2.3 RELEASE_ID=123 \
#     scripts/release-verify-upload.sh <assets-dir>
#
# The draft is addressed by its numeric release id, not by tag: GitHub's
# GET /repos/{repo}/releases/tags/{tag} answers 404 for a draft, so a tag
# lookup cannot see the release this run just created.
#
# GitHub can report a freshly uploaded asset's .digest as null for a moment, so
# the release API is polled a bounded number of times. Any missing digest or
# size, a different asset set, a wrong tag, a public (non-draft) release, or a
# mismatch fails closed; this script never publishes anything and never deletes
# a release.
#
# VERIFY_ATTEMPTS (default 12) and VERIFY_DELAY seconds (default 5) bound the
# polling. Transient 404/5xx answers and transport failures are retried inside
# that budget; any other HTTP answer fails at once. The assets directory must
# hold exactly the 17 release files.
set -eu

: "${REPO:?set REPO to owner/name}"
: "${TAG:?set TAG to the release tag}"
: "${RELEASE_ID:?set RELEASE_ID to the numeric id of the draft release}"
assets=${1:-}
[ -d "$assets" ] || {
	echo "usage: REPO=<owner/name> TAG=<tag> RELEASE_ID=<id> $0 <assets-dir>" >&2
	exit 2
}
case $RELEASE_ID in
'' | *[!0-9]*)
	echo "RELEASE_ID must be a numeric release id, got '$RELEASE_ID'" >&2
	exit 2
	;;
esac
api=${API_URL:-https://api.github.com}
attempts=${VERIFY_ATTEMPTS:-12}
delay=${VERIFY_DELAY:-5}

tmp=$(mktemp -d "${TMPDIR:-/tmp}/routelatch-verify.XXXXXX")
trap 'rm -rf "$tmp"' EXIT INT TERM
json="$tmp/release.json"

# fetch <file>: write the release JSON to <file>, print the HTTP status.
fetch() {
	if [ -n "${GH_TOKEN:-}" ]; then
		curl -sS -o "$1" -w '%{http_code}' \
			-H "Authorization: Bearer $GH_TOKEN" \
			-H 'Accept: application/vnd.github+json' \
			"$api/repos/$REPO/releases/$RELEASE_ID"
	else
		curl -sS -o "$1" -w '%{http_code}' \
			-H 'Accept: application/vnd.github+json' \
			"$api/repos/$REPO/releases/$RELEASE_ID"
	fi
}

# The release must be this tag's draft with exactly 17 assets and, for every
# one, a numeric size and a sha256: digest. Anything less is not yet verifiable.
complete() {
	jq -e --arg tag "$TAG" '
		(.tag_name == $tag)
		and (.draft == true)
		and (((.assets // []) | length) == 17)
		and all(.assets[];
			((.size | type) == "number") and (.size > 0)
			and ((.digest | type) == "string")
			and (.digest | startswith("sha256:")))
	' "$1" >/dev/null 2>&1
}

attempt=0
code=000
while :; do
	attempt=$((attempt + 1))
	code=$(fetch "$json") || code=000
	# Retry only transient states: an incomplete 200, 404 (the release may not
	# be visible for a moment), 5xx, and transport failures. Anything else is
	# final.
	if [ "$code" = 200 ]; then
		complete "$json" && break
	elif [ "$code" != 404 ] && [ "$code" != 000 ] &&
		{ [ "$code" -lt 500 ] || [ "$code" -gt 599 ]; }; then
		echo "the releases API answered HTTP $code for release id $RELEASE_ID; not publishing" >&2
		exit 1
	fi
	if [ "$attempt" -ge "$attempts" ]; then
		if [ "$code" = 200 ]; then
			jq -r '.assets[]?.name' "$json" 2>/dev/null | LC_ALL=C sort >"$tmp/api.names" || : >"$tmp/api.names"
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
		fi
		echo "release id $RELEASE_ID (HTTP $code) did not report the draft for" \
			"$TAG, the exact asset set and every sha256 digest after" \
			"$attempt checks; not publishing" >&2
		exit 1
	fi
	sleep "$delay"
done

[ "$(jq -r '.tag_name' "$json")" = "$TAG" ] ||
	{ echo "release id $RELEASE_ID does not carry tag $TAG"; exit 1; }
[ "$(jq -r '.draft' "$json")" = true ] ||
	{ echo "release id $RELEASE_ID is not a draft"; exit 1; }
jq -r '.assets[] | "\(.name)\t\(.size)\t\(.digest)"' "$json" |
	LC_ALL=C sort >"$tmp/assets.api"
for file in "$assets"/*; do
	printf '%s\t%s\tsha256:%s\n' "$(basename "$file")" "$(stat -c '%s' "$file")" \
		"$(sha256sum "$file" | awk '{print $1}')"
done | LC_ALL=C sort >"$tmp/assets.local"
diff -u "$tmp/assets.local" "$tmp/assets.api" ||
	{ echo "uploaded asset name/size/digest differs from the built files"; exit 1; }
echo "all 17 uploaded assets match the built name, size and sha256 digest"
