#!/usr/bin/env bash
# release-standard-lint controls. Runs the Go unit tests (planted negatives
# next to valid positives; TestMain fails the run if any registered finding
# ID has no driving negative test) and checks the CLI contract on scratch
# copies of the templates. Go caches live in the scratch root; the
# first run needs network access to download gopkg.in/yaml.v3 via GOPROXY.
set -eu -o pipefail
# shellcheck source=tests/lib.sh
. "$(dirname "$0")/lib.sh"
make_scratch
mkdir -p "$SCRATCH/go-build" "$SCRATCH/go-mod" "$SCRATCH/go-path" "$SCRATCH/bin"
export GOCACHE="$SCRATCH/go-build" GOMODCACHE="$SCRATCH/go-mod" GOPATH="$SCRATCH/go-path"
export GOFLAGS=-mod=readonly
cd "$REPO_ROOT"

# Proves the Go suite passes: every registered finding ID fires on at least
# one planted input that a test expects it on, the templates pass, and the
# reusable workflow meets its publish contract (see
# cmd/release-standard-lint/*_test.go). harness-control-test.sh shows the
# suite fails when individual rules are narrowed away.
if go test -count=1 ./cmd/release-standard-lint/ >"$SCRATCH/go-test.log" 2>&1; then
	pass go-unit-tests
else
	fail go-unit-tests "$(cat "$SCRATCH/go-test.log")"
fi

go build -o "$SCRATCH/bin/lint" ./cmd/release-standard-lint
LINT="$SCRATCH/bin/lint"

# expect_lint LABEL WANT_RC WANT_STDOUT_SUBSTRING ARGS...
expect_lint() {
	local label=$1 want_rc=$2 want=$3
	shift 3
	local rc=0
	"$LINT" "$@" >"$SCRATCH/lint.out" 2>"$SCRATCH/lint.err" || rc=$?
	if [ "$rc" -eq "$want_rc" ] && { [ -z "$want" ] || grep -Fq -- "$want" "$SCRATCH/lint.out" "$SCRATCH/lint.err"; }; then
		pass "$label"
	else
		fail "$label" "want rc=$want_rc '$want', got rc=$rc: $(cat "$SCRATCH/lint.out" "$SCRATCH/lint.err")"
	fi
}

# drift SRC DEST OLD NEW: copy with exactly one literal replacement, or fail.
drift() {
	local count
	count=$(grep -Fc -- "$3" "$1" || true)
	[ "$count" = 1 ] || {
		fail "plant:$3" "matches $count lines in $1, want 1"
		return 1
	}
	awk -v old="$3" -v new="$4" '{ i = index($0, old); if (i) $0 = substr($0, 1, i - 1) new substr($0, i + length(old)); print }' "$1" >"$2"
	! cmp -s "$1" "$2"
}

TEMPLATE="$REPO_ROOT/template/.goreleaser.yml"
WORKFLOW="$REPO_ROOT/template/release.yml"

# Proves the CLI positive: the template and caller workflow pass when
# placeholders are allowed, and an unfilled copy fails on placeholders.
expect_lint template-ok 0 'release-standard-lint: ok' -allow-placeholders -workflow "$WORKFLOW" "$TEMPLATE"
expect_lint template-placeholders 1 ': placeholder-left: ' -repository relux-works/__REPOSITORY_NAME__ "$TEMPLATE"

# Proves drift is reported by finding ID with exit 1, paired with the
# template positive above.
if drift "$TEMPLATE" "$SCRATCH/no-arch.yml" 'formats: [deb, rpm, archlinux]' 'formats: [deb, rpm]'; then
	expect_lint drift-nfpm-formats 1 ': nfpm-formats: ' -allow-placeholders "$SCRATCH/no-arch.yml"
fi
if drift "$TEMPLATE" "$SCRATCH/cask.yml" 'skip_upload: auto' 'skip_upload: false'; then
	expect_lint drift-cask-skip-upload 1 ': cask-skip-upload: ' -allow-placeholders "$SCRATCH/cask.yml"
fi
if drift "$WORKFLOW" "$SCRATCH/unpinned.yml" '@__STANDARD_SHA__' '@main'; then
	expect_lint drift-standard-unpinned 1 ': standard-workflow-unpinned: ' -allow-placeholders -workflow "$SCRATCH/unpinned.yml" "$TEMPLATE"
fi

# Proves the workflow inputs are enforced: a main package the config does not
# build fails, the matching one passes.
expect_lint expect-main-mismatch 1 ': build-main-mismatch: ' -allow-placeholders -main ./cmd/other "$TEMPLATE"
expect_lint expect-main-match 0 'release-standard-lint: ok' -allow-placeholders -main __MAIN_PACKAGE__ "$TEMPLATE"

# Proves usage and read errors exit 2 instead of passing: a missing file, no
# arguments, a caller config without -repository (only the template, with
# placeholders allowed, may omit it) and a -repository that is not OWNER/NAME.
expect_lint missing-file 2 '' -repository relux-works/x "$SCRATCH/absent.yml"
expect_lint no-arguments 2 ''
expect_lint no-repository 2 'release-standard-lint: -repository OWNER/NAME is required' "$TEMPLATE"
expect_lint bad-repository 2 'is not OWNER/NAME' -repository 'relux-works' "$TEMPLATE"

finish lint-test
