#!/bin/sh
# Release tag admission for the Relux release standard.
#
# Generalizes curator-agent-launcher's scripts/release-gate.sh. Admission only:
# it never builds, signs, pushes a tag or publishes anything, and it does not
# change refs or working files. Every refusal prints one line
#   tag-gate: REFUSED[<reason>]: <detail>
# on stderr and exits with the reason's own code:
#
#   2  usage                      bad or missing arguments
#   10 bad_tag_name               not vX.Y.Z or vX.Y.Z-rc.N, or not one line
#   11 prerelease_not_allowed     -rc.N tag while --allow-prerelease is false
#   12 not_a_tag_ref              --ref is not refs/tags/<tag>
#   13 tag_missing                refs/tags/<tag> does not exist
#   14 lightweight_tag            the ref does not name an annotated tag object
#   15 tag_name_mismatch          the tag object's own name differs from the ref
#   16 signers_file_missing       allowed_signers is not a regular file at the tag commit
#   17 unsigned_tag               no signature block in the tag object
#   18 non_ssh_signature          OpenPGP or X.509 signature marker (as Git
#                                 recognizes it, CRLF included); refused in
#                                 both modes, also with --require-signed false
#   19 untrusted_signature        git verify-tag failed against allowed_signers
#   20 commit_mismatch            the tag commit is not the checked-out HEAD
#   21 version_source_unreadable  the version source is missing or ambiguous
#   22 version_mismatch           tag differs from "v" + version source
#   23 main_ref_missing           --main-ref does not resolve to a commit
#   24 not_on_main                the tag commit is not an ancestor of main
#   25 unsafe_path                a repository path is absolute or escapes the tree
#
# The allowlist and the version source are read from the tag commit's tree,
# not from the working tree, so nothing that ran in the checkout can change them.
set -eu

me='tag-gate'
nl='
'

refuse() {
	# $1 exit code, $2 reason, $3 detail
	printf '%s: REFUSED[%s]: %s\n' "$me" "$2" "$3" >&2
	exit "$1"
}

usage() {
	refuse 2 usage "$1 (usage: tag-gate.sh --tag vX.Y.Z --version-file PATH --version-regex ERE [--repo DIR] [--ref refs/tags/vX.Y.Z] [--signers PATH] [--require-signed true|false] [--require-on-main true|false] [--main-ref REF] [--allow-prerelease true|false])"
}

bool_arg() {
	case $2 in
	true | false) ;;
	*) usage "$1 must be true or false" ;;
	esac
}

repo=.
tag=
ref=
signers=.github/release-signers
version_file=
version_regex=
require_signed=true
require_on_main=true
main_ref=refs/remotes/origin/main
allow_prerelease=false
tag_set=false

while [ $# -gt 0 ]; do
	case $1 in
	--repo | --tag | --ref | --signers | --version-file | --version-regex | \
		--require-signed | --require-on-main | --main-ref | --allow-prerelease)
		[ $# -ge 2 ] || usage "$1 needs a value"
		case $1 in
		--repo) repo=$2 ;;
		--tag) tag=$2 tag_set=true ;;
		--ref) ref=$2 ;;
		--signers) signers=$2 ;;
		--version-file) version_file=$2 ;;
		--version-regex) version_regex=$2 ;;
		--require-signed) bool_arg "$1" "$2" && require_signed=$2 ;;
		--require-on-main) bool_arg "$1" "$2" && require_on_main=$2 ;;
		--main-ref) main_ref=$2 ;;
		--allow-prerelease) bool_arg "$1" "$2" && allow_prerelease=$2 ;;
		esac
		shift 2
		;;
	*) usage "unknown argument: $1" ;;
	esac
done

[ "$tag_set" = true ] || usage '--tag is required'
[ -n "$version_file" ] || usage '--version-file is required'
[ -n "$version_regex" ] || usage '--version-regex is required'

# 1. Tag name syntax. Never interpret a ref as a Git option, never accept
# a multi-line value, leading zeros or any suffix other than -rc.N.
case $tag in
*"$nl"*) refuse 10 bad_tag_name 'tag name spans more than one line' ;;
esac
num='(0|[1-9][0-9]*)'
printf '%s\n' "$tag" | grep -Eqx "v$num\\.$num\\.$num(-rc\\.[1-9][0-9]*)?" ||
	refuse 10 bad_tag_name "expected vX.Y.Z or vX.Y.Z-rc.N, got '$tag'"
case $tag in
*-rc.*)
	[ "$allow_prerelease" = true ] ||
		refuse 11 prerelease_not_allowed "$tag is a prerelease and allow_prerelease is false"
	;;
esac

# 2. The triggering ref, when given, must be exactly this tag.
if [ -n "$ref" ] && [ "$ref" != "refs/tags/$tag" ]; then
	refuse 12 not_a_tag_ref "ref '$ref' is not refs/tags/$tag"
fi

safe_path() {
	# $1 label, $2 path relative to the repository root
	case $2 in
	'' | /* | .. | ../* | */.. | */../* | *"$nl"*)
		refuse 25 unsafe_path "$1 must be a relative path inside the repository: '$2'"
		;;
	esac
}
safe_path signers "$signers"
safe_path version_file "$version_file"

cd "$repo"
root=$(git rev-parse --show-toplevel)
cd "$root"

# 3. Annotated tag object whose own name equals the ref.
tag_ref=refs/tags/$tag
tag_oid=$(git rev-parse -q --verify "$tag_ref" 2>/dev/null) ||
	refuse 13 tag_missing "$tag_ref does not exist"
tag_type=$(git cat-file -t "$tag_oid")
[ "$tag_type" = tag ] ||
	refuse 14 lightweight_tag "$tag_ref names a $tag_type, not an annotated tag object"
header_name=$(git cat-file tag "$tag_oid" | sed -n '/^$/q; s/^tag //p')
[ "$header_name" = "$tag" ] ||
	refuse 15 tag_name_mismatch "tag object is named '$header_name', ref is '$tag'"

# 4. The tag names the checked-out commit.
commit=$(git rev-parse "$tag_oid^{commit}")
head=$(git rev-parse HEAD)
[ "$commit" = "$head" ] ||
	refuse 20 commit_mismatch "tag commit $commit is not HEAD $head"

tmp=
matches_file=
trap 'rm -f ${tmp:+"$tmp"} ${matches_file:+"$matches_file"}' EXIT
tmp=$(mktemp)
matches_file=$(mktemp)

# Copies a regular file from the tag commit's tree into $tmp, or fails.
tree_blob() {
	mode=$(git ls-tree "$commit" -- "$1" | awk 'NR == 1 { print $1 }')
	case $mode in
	100644 | 100755) git cat-file blob "$commit:$1" >"$tmp" ;;
	*) return 1 ;;
	esac
}

verify_tag_signature() {
	git -c gpg.format=ssh -c gpg.ssh.program=ssh-keygen \
		-c gpg.openpgp.program=false -c gpg.x509.program=false \
		-c gpg.ssh.allowedSignersFile="$tmp" \
		verify-tag "$1" >/dev/null 2>&1
}

# 5. Prohibited signature formats are refused in every mode: an OpenPGP or
# X.509 block is never accepted, not even when unsigned tags are allowed.
# Markers are recognized as Git does: a line that starts with the marker,
# whatever follows it (so a CRLF line ending, or trailing bytes, still
# counts). Git's OpenPGP markers are PGP SIGNATURE and PGP MESSAGE; X.509 is
# SIGNED MESSAGE.
body=$(git cat-file tag "$tag_oid")
if printf '%s\n' "$body" | grep -Eq -- '^-----BEGIN (PGP SIGNATURE|PGP MESSAGE|SIGNED MESSAGE)-----'; then
	refuse 18 non_ssh_signature "$tag carries an OpenPGP or X.509 signature block; only SSH signatures are accepted"
fi

# 6. SSH signature from a key in the allowlist (OpenPGP and X.509 disabled).
if [ "$require_signed" = true ]; then
	printf '%s\n' "$body" | grep -q -- '^-----BEGIN SSH SIGNATURE-----' ||
		refuse 17 unsigned_tag "$tag carries no SSH signature"
	tree_blob "$signers" ||
		refuse 16 signers_file_missing "$signers is not a regular file at $commit"
	verify_tag_signature "$tag_oid" ||
		refuse 19 untrusted_signature "$tag signature does not verify against $signers"
fi

# 7. Tag equals "v" + the version source (exactly one matching line). sed
# prints one record per matching line, prefixed with "x" so an empty capture
# is still a nonempty record (also on a last line without a newline), and the
# records are counted in a file before any command substitution can strip
# empty trailing ones.
tree_blob "$version_file" ||
	refuse 21 version_source_unreadable "$version_file is not a regular file at $commit"
d=$(printf '\001')
sed -n -E "s${d}${version_regex}${d}x\\1${d}p" "$tmp" >"$matches_file" 2>/dev/null ||
	refuse 21 version_source_unreadable "version regex is invalid or has no capture group"
count=$(awk 'END { print NR }' "$matches_file")
case $count in
0) refuse 21 version_source_unreadable "version regex matches no line of $version_file" ;;
1) ;;
*) refuse 21 version_source_unreadable "version regex matches $count lines of $version_file, want exactly one" ;;
esac
matches=$(cat "$matches_file")
matches=${matches#x}
printf '%s\n' "$matches" | grep -Eqx "$num\\.$num\\.$num(-rc\\.[1-9][0-9]*)?" ||
	refuse 21 version_source_unreadable "captured version '$matches' is not X.Y.Z or X.Y.Z-rc.N (anchor the regex with ^ and \$)"
[ "$tag" = "v$matches" ] ||
	refuse 22 version_mismatch "tag $tag does not equal v$matches from $version_file"

# 8. Optionally, the tag commit is already on main.
if [ "$require_on_main" = true ]; then
	main_oid=$(git rev-parse -q --verify "$main_ref^{commit}" 2>/dev/null) ||
		refuse 23 main_ref_missing "$main_ref does not resolve to a commit"
	git merge-base --is-ancestor "$commit" "$main_oid" ||
		refuse 24 not_on_main "$commit is not an ancestor of $main_ref"
fi

printf '%s: accepted %s (%s)\n' "$me" "$tag" "$commit"
