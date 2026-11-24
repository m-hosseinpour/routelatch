#!/bin/sh
# Guard the release publish job against touching a release it does not own.
#
# Usage:
#   REPO=owner/name TAG=v1.2.3 scripts/release-guard.sh check
#   REPO=owner/name TAG=v1.2.3 scripts/release-guard.sh delete-draft
#
# check        succeeds only when no release exists for TAG. An existing public
#              release or an existing draft is refused: a pre-existing draft is
#              never adopted or deleted automatically, because it may be a
#              partial upload or a deliberate manual draft.
# delete-draft deletes the release for TAG only when the API currently reports
#              it as a draft. A public release, a 404, or any API error leaves it
#              untouched; the workflow calls this only for a draft its own create
#              step made.
#
# API_URL defaults to the public GitHub API; GH_TOKEN adds the bearer header.
# This script does not build, upload or publish anything else.
set -eu

mode=${1:-}
: "${REPO:?set REPO to owner/name}"
: "${TAG:?set TAG to the release tag}"
api=${API_URL:-https://api.github.com}

usage() {
	echo "usage: REPO=<owner/name> TAG=<tag> $0 check|delete-draft" >&2
	exit 2
}

die() {
	echo "release guard FAILED: $*" >&2
	exit 1
}

# fetch <file>: write the release JSON to <file>, print the HTTP status.
fetch() {
	if [ -n "${GH_TOKEN:-}" ]; then
		curl -sS -o "$1" -w '%{http_code}' \
			-H "Authorization: Bearer $GH_TOKEN" \
			-H 'Accept: application/vnd.github+json' \
			"$api/repos/$REPO/releases/tags/$TAG"
	else
		curl -sS -o "$1" -w '%{http_code}' \
			-H 'Accept: application/vnd.github+json' \
			"$api/repos/$REPO/releases/tags/$TAG"
	fi
}

case $mode in
check)
	tmp=$(mktemp "${TMPDIR:-/tmp}/routelatch-guard.XXXXXX")
	trap 'rm -f "$tmp"' EXIT INT TERM
	code=$(fetch "$tmp") ||
		die "cannot read the release state for $TAG; refusing to continue on an unknown state"
	case $code in
	404)
		echo "no release exists for $TAG"
		exit 0
		;;
	200) ;;
	*)
		die "the releases API answered HTTP $code for $TAG; refusing to continue"
		;;
	esac
	id=$(jq -r '.id | select(. != null)' "$tmp") ||
		die "cannot parse the existing release for $TAG"
	[ -n "$id" ] || die "the releases API returned no release id for $TAG"
	if [ "$(jq -r '.draft' "$tmp")" = true ]; then
		echo "a draft release (id $id) already exists for $TAG; this run did not create it." >&2
		echo "Refusing to adopt or delete it. Inspect it, then delete it by hand" \
			"(gh release delete $TAG --yes) or publish/abandon it deliberately, and re-run." >&2
		exit 1
	fi
	echo "a public release (id $id) already exists for $TAG; refusing to change it." >&2
	exit 1
	;;
delete-draft)
	tmp=$(mktemp "${TMPDIR:-/tmp}/routelatch-guard.XXXXXX")
	trap 'rm -f "$tmp"' EXIT INT TERM
	code=$(fetch "$tmp") || {
		echo "cannot read the state of $TAG; leaving the release untouched" >&2
		exit 0
	}
	case $code in
	404)
		echo "no release exists for $TAG; nothing to delete"
		exit 0
		;;
	200) ;;
	*)
		echo "the releases API answered HTTP $code for $TAG; leaving the release untouched" >&2
		exit 0
		;;
	esac
	if [ "$(jq -r '.draft' "$tmp" 2>/dev/null || printf false)" != true ]; then
		echo "$TAG is not a draft; leaving it untouched" >&2
		exit 0
	fi
	echo "deleting the draft release this run created for $TAG"
	gh release delete "$TAG" --yes
	;;
*)
	usage
	;;
esac
