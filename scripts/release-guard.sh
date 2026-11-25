#!/bin/sh
# Guard the release publish job against touching a release it does not own.
#
# Usage:
#   REPO=owner/name TAG=v1.2.3 scripts/release-guard.sh check
#   REPO=owner/name TAG=v1.2.3 RELEASE_ID=123 scripts/release-guard.sh delete-draft
#
# check        succeeds only when no release exists for TAG. It lists the
#              repository's releases (drafts included, paginated) and refuses
#              when any release carries this tag. An existing public release or
#              an existing draft is never adopted or deleted automatically,
#              because a pre-existing draft may be a partial upload or a
#              deliberate manual draft. GitHub has no draft-by-tag endpoint, so
#              a tag lookup cannot see a draft; only the listing can.
# delete-draft deletes exactly the release id this run's create step reported,
#              and only after the id endpoint currently reports it as a draft
#              with this tag. A public release, a different tag, a 404, or any
#              API error leaves it untouched. GitHub has no draft-by-tag
#              endpoint, so a tag lookup or tag delete cannot reach a draft.
#
# API_URL defaults to the public GitHub API; GH_TOKEN adds the bearer header.
# This script does not build, upload or publish anything else.
set -eu

mode=${1:-}
: "${REPO:?set REPO to owner/name}"
: "${TAG:?set TAG to the release tag}"
api=${API_URL:-https://api.github.com}
release_id=${RELEASE_ID:-}
max_pages=100

usage() {
	echo "usage: REPO=<owner/name> TAG=<tag> $0 check" >&2
	echo "       REPO=<owner/name> TAG=<tag> RELEASE_ID=<id> $0 delete-draft" >&2
	exit 2
}

die() {
	echo "release guard FAILED: $*" >&2
	exit 1
}

# fetch_url <file> <url>: write the body to <file>, print the HTTP status.
fetch_url() {
	if [ -n "${GH_TOKEN:-}" ]; then
		curl -sS -o "$1" -w '%{http_code}' \
			-H "Authorization: Bearer $GH_TOKEN" \
			-H 'Accept: application/vnd.github+json' \
			"$2"
	else
		curl -sS -o "$1" -w '%{http_code}' \
			-H 'Accept: application/vnd.github+json' \
			"$2"
	fi
}

case $mode in
check)
	tmp=$(mktemp -d "${TMPDIR:-/tmp}/routelatch-guard.XXXXXX")
	trap 'rm -rf "$tmp"' EXIT INT TERM
	# Drafts are only visible in the listing, so read every page and fail
	# closed on anything but a complete listing.
	page=1
	while :; do
		file="$tmp/releases.$page.json"
		code=$(fetch_url "$file" \
			"$api/repos/$REPO/releases?per_page=100&page=$page") || code=000
		[ "$code" = 200 ] ||
			die "the releases API answered HTTP $code while listing releases for $TAG; refusing to continue on an unknown state"
		jq -e 'type == "array"' "$file" >/dev/null 2>&1 ||
			die "the releases API returned a non-array page while listing releases for $TAG; refusing to continue"
		count=$(jq 'length' "$file")
		[ "$count" -eq 100 ] || break
		page=$((page + 1))
		[ "$page" -le "$max_pages" ] ||
			die "the releases listing for $TAG exceeded $max_pages pages; refusing to continue"
	done
	jq -s 'add' "$tmp"/releases.*.json >"$tmp/all.json" ||
		die "cannot combine the release listing for $TAG; refusing to continue"
	jq -r --arg tag "$TAG" '
		.[] | select(.tag_name == $tag)
		| "\(.id)\t\(if .draft then "draft" else "public" end)"
	' "$tmp/all.json" >"$tmp/matches" ||
		die "cannot read the existing releases for $TAG"
	if [ -s "$tmp/matches" ]; then
		while IFS="$(printf '\t')" read -r id kind; do
			if [ "$kind" = draft ]; then
				echo "a draft release (id $id) already exists for $TAG; this run did not create it." >&2
				echo "Refusing to adopt or delete it. Inspect it, then delete it by id" \
					"(gh api -X DELETE repos/$REPO/releases/$id) or publish/abandon it" \
					"deliberately, and re-run." >&2
			else
				echo "a public release (id $id) already exists for $TAG; refusing to change it." >&2
			fi
		done <"$tmp/matches"
		exit 1
	fi
	echo "no release exists for $TAG"
	;;
delete-draft)
	case $release_id in
	'' | *[!0-9]*)
		echo "no numeric RELEASE_ID was given; leaving the release untouched" >&2
		exit 1
		;;
	esac
	tmp=$(mktemp -d "${TMPDIR:-/tmp}/routelatch-guard.XXXXXX")
	trap 'rm -rf "$tmp"' EXIT INT TERM
	code=$(fetch_url "$tmp/release.json" \
		"$api/repos/$REPO/releases/$release_id") || code=000
	case $code in
	404)
		echo "release id $release_id does not exist; nothing to delete"
		exit 0
		;;
	200) ;;
	*)
		echo "the releases API answered HTTP $code for release id $release_id; leaving it untouched" >&2
		exit 0
		;;
	esac
	if [ "$(jq -r '.tag_name // empty' "$tmp/release.json" 2>/dev/null || true)" != "$TAG" ]; then
		echo "release id $release_id does not carry tag $TAG; leaving it untouched" >&2
		exit 0
	fi
	if [ "$(jq -r '.draft' "$tmp/release.json" 2>/dev/null || printf false)" != true ]; then
		echo "release id $release_id is not a draft; leaving it untouched" >&2
		exit 0
	fi
	echo "deleting the draft release (id $release_id) this run created for $TAG"
	if [ -n "${GH_TOKEN:-}" ]; then
		code=$(curl -sS -o /dev/null -w '%{http_code}' -X DELETE \
			-H "Authorization: Bearer $GH_TOKEN" \
			-H 'Accept: application/vnd.github+json' \
			"$api/repos/$REPO/releases/$release_id") || code=000
	else
		code=$(curl -sS -o /dev/null -w '%{http_code}' -X DELETE \
			-H 'Accept: application/vnd.github+json' \
			"$api/repos/$REPO/releases/$release_id") || code=000
	fi
	[ "$code" = 204 ] || {
		echo "deleting release id $release_id answered HTTP $code; it may still exist" >&2
		exit 1
	}
	;;
*)
	usage
	;;
esac
