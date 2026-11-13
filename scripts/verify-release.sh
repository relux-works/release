#!/bin/sh
# Consumer-side verification of a release built by the Relux release standard.
#
# Downloads nothing itself: pass a directory that already holds the downloaded
# assets, including checksums.txt, checksums.txt.sig and checksums.txt.pem.
# cosign contacts the Sigstore transparency log, and the optional --attest
# step asks the GitHub API for attestations; nothing else touches the network.
#
# Order: authenticate checksums.txt with cosign (the certificate must come from
# the SHARED go-release.yml running for --repo at refs/tags/--tag), then check
# every downloaded asset against it, then optionally verify GitHub build
# provenance. Exit codes:
#
#   2  usage                    bad or missing arguments
#   30 tool_missing             cosign, gh or a SHA-256 tool is not installed
#   31 signature_files_missing  checksums.txt, .sig or .pem is missing
#   32 cosign_verify_failed     the checksums signature or identity is wrong
#   33 checksum_entry_invalid   checksums.txt has a malformed or unsafe line
#   34 no_assets                no listed asset is present in the directory
#   35 checksum_mismatch        an asset does not match checksums.txt
#   36 unlisted_asset           a file in the directory is not covered by checksums.txt
#   37 attestation_failed       gh attestation verify failed for an asset
set -eu

me='verify-release'
# The shared standard repository (placeholder name; see NOTES.md).
standard_repo=${RELEASE_STANDARD_REPOSITORY:-relux-works/release}
workflow_path=.github/workflows/go-release.yml

refuse() {
	printf '%s: REFUSED[%s]: %s\n' "$me" "$2" "$3" >&2
	exit "$1"
}

usage() {
	refuse 2 usage "$1 (usage: verify-release.sh --dir DIR --repo OWNER/NAME --tag vX.Y.Z [--standard-ref REF] [--attest] [--allow-unlisted])"
}

dir=
repo=
tag=
standard_ref=
attest=false
allow_unlisted=false
while [ $# -gt 0 ]; do
	case $1 in
	--dir | --repo | --tag | --standard-ref)
		[ $# -ge 2 ] || usage "$1 needs a value"
		case $1 in
		--dir) dir=$2 ;;
		--repo) repo=$2 ;;
		--tag) tag=$2 ;;
		--standard-ref) standard_ref=$2 ;;
		esac
		shift 2
		;;
	--attest) attest=true && shift ;;
	--allow-unlisted) allow_unlisted=true && shift ;;
	*) usage "unknown argument: $1" ;;
	esac
done
[ -n "$dir" ] && [ -d "$dir" ] || usage '--dir must name an existing directory'
printf '%s\n' "$repo" | grep -Eqx '[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+' ||
	usage '--repo must be OWNER/NAME'
printf '%s\n' "$tag" | grep -Eqx 'v[0-9]+\.[0-9]+\.[0-9]+(-rc\.[0-9]+)?' ||
	usage '--tag must be vX.Y.Z or vX.Y.Z-rc.N'
dir=$(cd "$dir" && pwd -P)

command -v cosign >/dev/null 2>&1 || refuse 30 tool_missing 'cosign is not installed'
if command -v sha256sum >/dev/null 2>&1; then
	sha_check() { sha256sum -c "$1"; }
elif command -v shasum >/dev/null 2>&1; then
	sha_check() { shasum -a 256 -c "$1"; }
else
	refuse 30 tool_missing 'neither sha256sum nor shasum is installed'
fi
if [ "$attest" = true ]; then
	command -v gh >/dev/null 2>&1 || refuse 30 tool_missing 'gh is required for --attest'
fi

for f in checksums.txt checksums.txt.sig checksums.txt.pem; do
	[ -f "$dir/$f" ] || refuse 31 signature_files_missing "$dir/$f is missing"
done

# 1. The checksums file was signed by the shared workflow, run for this
# caller repository on this tag. Dots are escaped for the identity regex.
escaped_repo=$(printf '%s' "$standard_repo" | sed 's/\./\\./g')
escaped_path=$(printf '%s' "$workflow_path" | sed 's/\./\\./g')
if [ -n "$standard_ref" ]; then
	ref_re=$(printf '%s' "$standard_ref" | sed 's/[].[^$*+?(){}|\\]/\\&/g')
else
	ref_re='.+'
fi
identity_re="^https://github\\.com/$escaped_repo/$escaped_path@$ref_re\$"
cosign verify-blob \
	--certificate "$dir/checksums.txt.pem" \
	--signature "$dir/checksums.txt.sig" \
	--certificate-identity-regexp "$identity_re" \
	--certificate-oidc-issuer https://token.actions.githubusercontent.com \
	--certificate-github-workflow-repository "$repo" \
	--certificate-github-workflow-ref "refs/tags/$tag" \
	"$dir/checksums.txt" ||
	refuse 32 cosign_verify_failed "checksums.txt is not signed by $standard_repo/$workflow_path for $repo at $tag"

# 2. Every line is "<sha256>  <plain file name>"; no paths, no binary marker.
if grep -Evqx '[0-9a-f]{64}  [A-Za-z0-9._+-]+' "$dir/checksums.txt"; then
	refuse 33 checksum_entry_invalid 'checksums.txt has a line that is not "<sha256>  <file name>"'
fi

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
sed 's/^[0-9a-f]*  //' "$dir/checksums.txt" >"$work/names"
: >"$work/present"
while IFS= read -r line; do
	name=${line#*  }
	if [ -f "$dir/$name" ]; then
		printf '%s\n' "$line" >>"$work/present"
	fi
done <"$dir/checksums.txt"
[ -s "$work/present" ] || refuse 34 no_assets "none of the files listed in checksums.txt is in $dir"

(cd "$dir" && sha_check "$work/present") ||
	refuse 35 checksum_mismatch 'at least one asset does not match checksums.txt'

# 3. Anything else in the directory is unverified; refuse unless told not to.
unlisted=
for path in "$dir"/* "$dir"/.[!.]*; do
	[ -f "$path" ] || continue
	base=${path##*/}
	case $base in checksums.txt | checksums.txt.sig | checksums.txt.pem) continue ;; esac
	grep -Fqx -- "$base" "$work/names" || unlisted="$unlisted $base"
done
if [ -n "$unlisted" ]; then
	if [ "$allow_unlisted" = true ]; then
		printf '%s: NOT VERIFIED (not in checksums.txt):%s\n' "$me" "$unlisted" >&2
	else
		refuse 36 unlisted_asset "not covered by checksums.txt:$unlisted"
	fi
fi

# 4. Optional GitHub build provenance, signed by the shared workflow.
if [ "$attest" = true ]; then
	sed 's/^[0-9a-f]*  //' "$work/present" >"$work/attest"
	echo checksums.txt >>"$work/attest"
	while IFS= read -r name; do
		case $name in
		*.tar.gz | *.zip | *.deb | *.rpm | *.pkg.tar.zst | checksums.txt) ;;
		*) continue ;;
		esac
		gh attestation verify "$dir/$name" --repo "$repo" \
			--signer-workflow "$standard_repo/$workflow_path" >/dev/null ||
			refuse 37 attestation_failed "no valid provenance for $name"
	done <"$work/attest"
fi

printf '%s: verified %s %s (%s)\n' "$me" "$repo" "$tag" "$(wc -l <"$work/present" | tr -d ' ') assets"
