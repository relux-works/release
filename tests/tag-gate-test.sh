#!/usr/bin/env bash
# Tag-gate controls with real git and ssh-keygen in a throwaway repository.
# No network, no host keys, no real tags: both keys are generated here.
# Every refusal must exit with its own code, print exactly one named
# REFUSED line, print nothing on stdout and leave refs and files unchanged.
# TAG_GATE overrides the gate under test; harness-control-test.sh uses it to
# prove that this harness fails on a gate with a planted bypass.
set -eu -o pipefail
# shellcheck source=tests/lib.sh
. "$(dirname "$0")/lib.sh"
GATE=${TAG_GATE:-$REPO_ROOT/scripts/tag-gate.sh}
make_scratch

ssh-keygen -q -t ed25519 -N '' -C trusted-fixture -f "$SCRATCH/trusted"
ssh-keygen -q -t ed25519 -N '' -C foreign-fixture -f "$SCRATCH/foreign"
REPO="$SCRATCH/work/repo"
git init -q -b main "$REPO"
cd "$REPO"
# Synthetic identity; no operator identity, credential or release key.
git config user.name 'Tag gate fixture'
git config user.email 'tag-gate-fixture@example.invalid'
git config commit.gpgsign false
git config tag.gpgsign false
mkdir -p .github
printf 'fixture@example.invalid namespaces="git" %s\n' "$(cat "$SCRATCH/trusted.pub")" >.github/release-signers
printf 'package main\n\nvar buildVersion = "1.2.3"\n' >version.go
git add .github version.go
git commit -q -m 'Fixture at 1.2.3'
git commit -q --allow-empty -m 'Second fixture commit at 1.2.3'
# Stands in for the fetched origin/main of a real checkout.
git update-ref refs/remotes/origin/main HEAD
REGEX='^var buildVersion = "([^"]*)"$'

# Signs with an explicit key file; command-line -c beats inherited config.
sign_git() {
	local key=$1
	shift
	git -c gpg.format=ssh -c gpg.ssh.program=ssh-keygen -c user.signingkey="$key" "$@"
}

# make_tag NAME signed|foreign|annotated|lightweight [TARGET]
make_tag() {
	local name=$1 kind=$2 target=${3:-HEAD}
	git tag -d "$name" >/dev/null 2>&1 || true
	case $kind in
	signed) sign_git "$SCRATCH/trusted" tag -s "$name" -m "Release $name" "$target" ;;
	foreign) sign_git "$SCRATCH/foreign" tag -s "$name" -m "Release $name" "$target" ;;
	annotated) git tag -a "$name" -m "Release $name" "$target" ;;
	lightweight) git tag "$name" "$target" ;;
	esac
}

state() {
	git show-ref
	git status --porcelain --untracked-files=all
}

run_gate() {
	"$GATE" --repo "$REPO" --signers .github/release-signers \
		--version-file version.go --version-regex "$REGEX" "$@"
}

expect_accept() {
	local label=$1
	shift
	local before rc=0
	before=$(state)
	run_gate "$@" >"$SCRATCH/out.log" 2>"$SCRATCH/err.log" || rc=$?
	if [ "$rc" -eq 0 ] && grep -q '^tag-gate: accepted ' "$SCRATCH/out.log" &&
		[ "$before" = "$(state)" ]; then
		pass "$label"
	else
		fail "$label" "expected acceptance, rc=$rc, stderr: $(cat "$SCRATCH/err.log")"
	fi
}

expect_refusal() {
	local label=$1 code=$2 reason=$3
	shift 3
	local before rc=0
	before=$(state)
	run_gate "$@" >"$SCRATCH/out.log" 2>"$SCRATCH/err.log" || rc=$?
	if [ "$rc" -eq "$code" ] &&
		grep -Fq "tag-gate: REFUSED[$reason]: " "$SCRATCH/err.log" &&
		[ "$(wc -l <"$SCRATCH/err.log" | tr -d ' ')" = 1 ] &&
		[ ! -s "$SCRATCH/out.log" ] &&
		[ "$before" = "$(state)" ]; then
		pass "$label"
	else
		fail "$label" "expected exit $code REFUSED[$reason], rc=$rc, stderr: $(cat "$SCRATCH/err.log")"
	fi
}

# Valid positive paired with each refusal: the trusted signed tag passes again
# in the same repository state, so the refusal came from the planted defect.
pair_control() {
	make_tag v1.2.3 signed
	expect_accept "$1-control" --tag v1.2.3
}

# Proves the baseline: a trusted SSH-signed annotated tag on main that matches
# the version source and HEAD is accepted (guards against a reject-all gate).
make_tag v1.2.3 signed
expect_accept trusted-signed --tag v1.2.3 --ref refs/tags/v1.2.3

# Proves an annotated but unsigned tag is refused when signatures are
# required, and accepted only when the caller opts out.
make_tag v1.2.3 annotated
expect_refusal unsigned 17 unsigned_tag --tag v1.2.3
expect_accept unsigned-opt-out --tag v1.2.3 --require-signed false
pair_control unsigned

# Proves a valid SSH signature from a key outside the allowlist is refused.
make_tag v1.2.3 foreign
expect_refusal foreign-signer 19 untrusted_signature --tag v1.2.3
pair_control foreign-signer

# Proves a signed tag object edited after signing no longer verifies.
make_tag v1.2.3 signed
original=$(git rev-parse refs/tags/v1.2.3)
tampered=$(git cat-file tag "$original" | sed 's/^Release v1.2.3$/Tampered v1.2.3/' | git hash-object -t tag -w --stdin)
[ "$tampered" != "$original" ] || fail tampered-plant 'tamper did not change the tag object'
git update-ref refs/tags/v1.2.3 "$tampered"
expect_refusal tampered-signature 19 untrusted_signature --tag v1.2.3
pair_control tampered-signature

# synthetic_tag BEGIN_LINE END_LINE: writes a tag object for HEAD named
# v1.2.3 whose signature block uses the given armor lines, and points the ref
# at it. The block is synthetic; no OpenPGP or X.509 key is involved.
synthetic_tag() {
	local oid
	oid=$(printf 'object %s\ntype commit\ntag v1.2.3\ntagger Fixture <fixture@example.invalid> 1700000000 +0000\n\nRelease v1.2.3\n%s\n\nZml4dHVyZQ==\n%s\n' \
		"$(git rev-parse HEAD)" "$1" "$2" | git hash-object -t tag -w --stdin)
	git update-ref refs/tags/v1.2.3 "$oid"
}

# Proves OpenPGP and X.509 signature blocks are refused by name in both
# modes: required signatures, and the documented unsigned-tag opt-out, which
# permits an absent signature but never a prohibited format. The opt-out
# positives next to them (an unsigned annotated tag and a trusted SSH-signed
# tag with --require-signed false) show the opt-out itself still works.
synthetic_tag '-----BEGIN PGP SIGNATURE-----' '-----END PGP SIGNATURE-----'
expect_refusal pgp-signature 18 non_ssh_signature --tag v1.2.3
expect_refusal pgp-signature-opt-out 18 non_ssh_signature --tag v1.2.3 --require-signed false
synthetic_tag '-----BEGIN SIGNED MESSAGE-----' '-----END SIGNED MESSAGE-----'
expect_refusal x509-signature 18 non_ssh_signature --tag v1.2.3
expect_refusal x509-signature-opt-out 18 non_ssh_signature --tag v1.2.3 --require-signed false
make_tag v1.2.3 annotated
expect_accept prohibited-format-opt-out-unsigned-control --tag v1.2.3 --require-signed false
make_tag v1.2.3 signed
expect_accept prohibited-format-opt-out-ssh-control --tag v1.2.3 --require-signed false
pair_control prohibited-formats

# Proves a lightweight tag is refused, also when signatures are not required.
make_tag v1.2.3 lightweight
expect_refusal lightweight 14 lightweight_tag --tag v1.2.3
expect_refusal lightweight-unsigned-mode 14 lightweight_tag --tag v1.2.3 --require-signed false
pair_control lightweight

# Proves a trusted signed tag that differs from the version source is refused.
make_tag v1.2.4 signed
expect_refusal version-mismatch 22 version_mismatch --tag v1.2.4
git tag -d v1.2.4 >/dev/null
pair_control version-mismatch

# Proves a ref whose tag object carries another name is refused.
make_tag v1.2.3 signed
git update-ref refs/tags/v1.2.9 refs/tags/v1.2.3
expect_refusal aliased-tag 15 tag_name_mismatch --tag v1.2.9
git update-ref -d refs/tags/v1.2.9
pair_control aliased-tag

# Proves a trusted tag on another commit than the checkout is refused.
make_tag v1.2.3 signed HEAD~1
expect_refusal commit-mismatch 20 commit_mismatch --tag v1.2.3
pair_control commit-mismatch

# Proves a trusted tag on a commit that main does not contain is refused, is
# accepted only with require_on_main=false, and that an absent main ref is
# its own refusal.
git checkout -q -b side
git commit -q --allow-empty -m 'Side commit at 1.2.3'
make_tag v1.2.3 signed
expect_refusal not-on-main 24 not_on_main --tag v1.2.3
expect_accept not-on-main-opt-out --tag v1.2.3 --require-on-main false
expect_refusal main-ref-missing 23 main_ref_missing --tag v1.2.3 --main-ref refs/remotes/origin/absent
git checkout -q main
pair_control not-on-main

# Proves missing tags, malformed names and a non-tag trigger ref are refused
# before anything is read from the repository.
expect_refusal missing-tag 13 tag_missing --tag v1.2.8
expect_refusal option-as-tag 10 bad_tag_name --tag --help
expect_refusal leading-zero 10 bad_tag_name --tag v01.2.3
expect_refusal other-suffix 10 bad_tag_name --tag v1.2.3-beta.1
expect_refusal multiline 10 bad_tag_name --tag "$(printf 'v1.2.3\nv1.2.4')"
expect_refusal branch-ref 12 not_a_tag_ref --tag v1.2.3 --ref refs/heads/v1.2.3
expect_refusal usage-missing-tag 2 usage
pair_control names

# Proves unsafe paths, an absent allowlist and unreadable version sources
# (no match, no capture group, unanchored partial match) are refused by name.
expect_refusal signers-outside 25 unsafe_path --tag v1.2.3 --signers ../release-signers
expect_refusal signers-absolute 25 unsafe_path --tag v1.2.3 --signers /etc/hosts
expect_refusal signers-absent 16 signers_file_missing --tag v1.2.3 --signers .github/absent
expect_refusal regex-no-match 21 version_source_unreadable --tag v1.2.3 --version-regex '^const none = "([^"]*)"$'
expect_refusal regex-no-group 21 version_source_unreadable --tag v1.2.3 --version-regex '^var buildVersion'
expect_refusal regex-unanchored 21 version_source_unreadable --tag v1.2.3 --version-regex 'buildVersion = "([^"]*)"'
pair_control paths

# set_version_source CONTENT: commits CONTENT (printf format, no arguments)
# as version.go, moves the stand-in origin/main to it and tags it signed.
set_version_source() {
	# shellcheck disable=SC2059 # CONTENT is a fixed fixture format
	printf "$1" >version.go
	git commit -q -am "Fixture version source variant"
	git update-ref refs/remotes/origin/main HEAD
	make_tag v1.2.3 signed
}

# Proves the version source must match exactly one line, counted
# independently of the captured contents: an empty capture after the valid
# one (also on a last line without a newline), an empty capture before it,
# and two nonempty captures are each refused, while a single valid line in
# the same layout is accepted.
set_version_source 'package main\n\nvar buildVersion = "1.2.3"\nvar buildVersion = ""\n'
expect_refusal version-trailing-empty 21 version_source_unreadable --tag v1.2.3
set_version_source 'package main\n\nvar buildVersion = "1.2.3"\nvar buildVersion = ""'
expect_refusal version-trailing-empty-no-newline 21 version_source_unreadable --tag v1.2.3
set_version_source 'package main\n\nvar buildVersion = ""\nvar buildVersion = "1.2.3"\n'
expect_refusal version-leading-empty 21 version_source_unreadable --tag v1.2.3
set_version_source 'package main\n\nvar buildVersion = "1.2.3"\nvar buildVersion = "1.2.3"\n'
expect_refusal version-two-nonempty 21 version_source_unreadable --tag v1.2.3
set_version_source 'package main\n\nvar buildVersion = ""\n'
expect_refusal version-single-empty 21 version_source_unreadable --tag v1.2.3
set_version_source 'package main\n\nvar other = ""\nvar buildVersion = "1.2.3"'
expect_accept version-single-valid-no-newline --tag v1.2.3
set_version_source 'package main\n\nvar buildVersion = "1.2.3"\n'
expect_accept version-single-valid --tag v1.2.3

# Proves a -rc.N tag is refused unless the caller allows prereleases, and
# that an allowed, signed, matching prerelease passes.
printf 'package main\n\nvar buildVersion = "1.3.0-rc.1"\n' >version.go
git commit -q -am 'Fixture at 1.3.0-rc.1'
git update-ref refs/remotes/origin/main HEAD
make_tag v1.3.0-rc.1 signed
expect_refusal prerelease-not-allowed 11 prerelease_not_allowed --tag v1.3.0-rc.1
expect_accept prerelease-allowed --tag v1.3.0-rc.1 --allow-prerelease true

finish tag-gate-test
