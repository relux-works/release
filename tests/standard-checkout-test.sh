#!/usr/bin/env bash
# standard-checkout-check.sh controls with real git in a throwaway repository.
# Each modification of a clean checkout must be refused with its own code,
# and the restored checkout must pass again, so a refusal comes from the
# planted change and not from a check that rejects everything.
set -eu -o pipefail
# shellcheck source=tests/lib.sh
. "$(dirname "$0")/lib.sh"
CHECK=${STANDARD_CHECK:-$REPO_ROOT/scripts/standard-checkout-check.sh}
make_scratch

REPO="$SCRATCH/work/standard"
git init -q -b main "$REPO"
cd "$REPO"
git config user.name 'Standard checkout fixture'
git config user.email 'standard-checkout-fixture@example.invalid'
git config commit.gpgsign false
mkdir -p scripts
printf '#!/bin/sh\nexit 0\n' >scripts/signing-gate.sh
chmod +x scripts/signing-gate.sh
printf 'ignored/\n' >.gitignore
git add .
git commit -q -m 'Standard fixture'
SHA=$(git rev-parse HEAD)

# expect_check LABEL WANT_RC WANT_TEXT [SHA]
expect_check() {
	local label=$1 want_rc=$2 want=$3 sha=${4:-$SHA} rc=0 before
	before=$(git status --porcelain --untracked-files=all --ignored)
	"$CHECK" "$REPO" "$sha" >"$SCRATCH/out.log" 2>&1 || rc=$?
	if [ "$rc" -eq "$want_rc" ] && grep -Fq -- "$want" "$SCRATCH/out.log" &&
		[ "$before" = "$(git status --porcelain --untracked-files=all --ignored)" ]; then
		pass "$label"
	else
		fail "$label" "want rc=$want_rc '$want', got rc=$rc: $(cat "$SCRATCH/out.log")"
	fi
}

clean() {
	git checkout -q -- .
	chmod +x scripts/signing-gate.sh
	rm -rf ignored scripts/extra.sh
	expect_check "$1-control" 0 'standard-checkout-check: '
}

# Proves the baseline: an untouched checkout of SHA passes.
expect_check clean 0 'standard-checkout-check: '

# Proves a rewritten shared script is refused (the earlier-hook route).
printf '#!/bin/sh\nexit 0 # replaced\n' >scripts/signing-gate.sh
expect_check modified 31 'REFUSED[standard_tree_modified]'
clean modified

# Proves an added untracked file, an added ignored file, a removed file and a
# changed file mode are each refused.
printf 'x\n' >scripts/extra.sh
expect_check untracked 31 'REFUSED[standard_tree_modified]'
clean untracked
mkdir -p ignored && printf 'x\n' >ignored/file
expect_check ignored 31 'REFUSED[standard_tree_modified]'
clean ignored
rm scripts/signing-gate.sh
expect_check removed 31 'REFUSED[standard_tree_modified]'
clean removed
chmod -x scripts/signing-gate.sh
expect_check mode 31 'REFUSED[standard_tree_modified]'
clean mode

# Proves a checkout of another commit, a malformed SHA and missing arguments
# are refused by name.
expect_check other-sha 30 'REFUSED[standard_sha_mismatch]' 0123456789abcdef0123456789abcdef01234567
expect_check short-sha 2 'REFUSED[usage]' "${SHA:0:12}"
rc=0
"$CHECK" "$REPO" >"$SCRATCH/out.log" 2>&1 || rc=$?
if [ "$rc" -eq 2 ] && grep -Fq 'REFUSED[usage]' "$SCRATCH/out.log"; then
	pass missing-argument
else
	fail missing-argument "want rc=2 usage, got rc=$rc: $(cat "$SCRATCH/out.log")"
fi
clean arguments

finish standard-checkout-test
