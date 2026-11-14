#!/bin/sh
# Confirms that a checkout of the release standard is exactly a given commit:
# HEAD is that commit and the working tree, including untracked and ignored
# files and file modes, hashes to that commit's tree. go-release.yml runs it
# immediately before GoReleaser, so the scripts GoReleaser is pointed at are
# the ones of job.workflow_sha.
#
#   standard-checkout-check.sh DIR SHA
#
#   exit 0  checkout matches
#   exit 2  usage                     bad arguments
#   exit 30 standard_sha_mismatch     HEAD is not SHA
#   exit 31 standard_tree_modified    working tree differs from SHA's tree
#
# Only DIR/.git is written (a throwaway index and the blobs of changed files).
{ set +x; } 2>/dev/null
set -eu

me='standard-checkout-check'

refuse() {
	printf '%s: REFUSED[%s]: %s\n' "$me" "$2" "$3" >&2
	exit "$1"
}

[ $# -eq 2 ] || refuse 2 usage 'usage: standard-checkout-check.sh DIR SHA'
dir=$1
sha=$2
printf '%s\n' "$sha" | grep -Eqx '[0-9a-f]{40}' || refuse 2 usage 'SHA must be a full 40-hex commit id'

head=$(git -C "$dir" rev-parse --verify -q 'HEAD^{commit}' 2>/dev/null) ||
	refuse 30 standard_sha_mismatch "$dir has no HEAD commit"
[ "$head" = "$sha" ] ||
	refuse 30 standard_sha_mismatch "$dir HEAD $head is not $sha"

index=$(mktemp -d)
trap 'rm -rf "$index"' EXIT
GIT_INDEX_FILE="$index/index"
export GIT_INDEX_FILE
git -C "$dir" -c core.fileMode=true add --all --force -- . >/dev/null 2>&1 ||
	refuse 31 standard_tree_modified "cannot hash the working tree of $dir"
actual=$(git -C "$dir" write-tree)
expected=$(git -C "$dir" rev-parse --verify "$sha^{tree}")
[ "$actual" = "$expected" ] ||
	refuse 31 standard_tree_modified "working tree of $dir ($actual) is not the tree of $sha ($expected)"

printf '%s: %s matches %s\n' "$me" "$dir" "$sha"
