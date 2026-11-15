#!/usr/bin/env bash
# Instrument controls: proves the gate harnesses and the Go lint suite are
# not blind. Each control copies a gate (or the lint module) into scratch, so
# tracked files are never edited, plants a bypass or narrowing, confirms the
# plant applied, reruns the full harness against the mutant and requires it
# to FAIL on the exact case the plant defeats. A plant that did not apply, or
# a harness that passes, fails here.
set -eu -o pipefail
# shellcheck source=tests/lib.sh
. "$(dirname "$0")/lib.sh"
make_scratch
mkdir -p "$SCRATCH/mutants"

# plant_mutant SOURCE DEST SED_EXPR EXPECTED_LINE
plant_mutant() {
	local source=$1 dest=$2 expr=$3 expected=$4
	sed "$expr" "$source" >"$dest"
	chmod +x "$dest"
	if cmp -s "$source" "$dest" || ! grep -Fqx -- "$expected" "$dest"; then
		return 1
	fi
}

# plant_literal SOURCE DEST OLD_LINE NEW_LINE: copy SOURCE with the one line
# equal to OLD_LINE replaced, or fail when OLD_LINE is not exactly one line.
# Values pass through ENVIRON, so backslashes and $ stay literal.
plant_literal() {
	local count
	count=$(grep -Fxc -- "$3" "$1" || true)
	[ "$count" = 1 ] || return 1
	OLD=$3 NEW=$4 awk '$0 == ENVIRON["OLD"] { print ENVIRON["NEW"]; next } { print }' "$1" >"$2"
	chmod +x "$2"
	! cmp -s "$1" "$2"
}

# control LABEL HARNESS ENV_NAME MUTANT EXPECTED_FAIL_PREFIX
control() {
	local label=$1 harness=$2 env_name=$3 mutant=$4 expected=$5
	local rc=0
	env "$env_name=$mutant" bash "$TESTS_DIR/$harness" >"$SCRATCH/$label.log" 2>&1 || rc=$?
	if [ "$rc" -ne 0 ] && grep -q "^FAIL $expected" "$SCRATCH/$label.log"; then
		pass "$label"
	else
		fail "$label" "harness did not catch the planted bypass (rc=$rc); log: $SCRATCH/$label.log"
	fi
}

# Proves tag-gate-test.sh catches a gate that skips signature verification:
# the foreign-signer case must fail against that mutant.
tag_mutant="$SCRATCH/mutants/tag-gate.sh"
# shellcheck disable=SC2016 # literal $ in the sed program and expected line
if plant_mutant "$REPO_ROOT/scripts/tag-gate.sh" "$tag_mutant" \
	's/^\([[:space:]]*\)verify_tag_signature "\$tag_oid" ||$/\1true ||/' \
	"$(printf '\ttrue ||')"; then
	control tag-gate-signature-bypass tag-gate-test.sh TAG_GATE "$tag_mutant" 'foreign-signer:'
else
	fail tag-gate-signature-bypass 'plant did not apply to scripts/tag-gate.sh'
fi

# Proves signing-gate-test.sh catches a gate that lets partial signing
# configuration through: the partial cases must fail against that mutant.
signing_mutant="$SCRATCH/mutants/signing-gate.sh"
# shellcheck disable=SC2016 # literal $ in the sed program and expected line
if plant_mutant "$REPO_ROOT/scripts/signing-gate.sh" "$signing_mutant" \
	's/^\([[:space:]]*\)refuse 3 partial_signing_config /\1exit 0; refuse 3 partial_signing_config /' \
	"$(printf '\texit 0; refuse 3 partial_signing_config "$present of 5 signing fields present; set all five or none; missing:$missing"')"; then
	control signing-gate-partial-bypass signing-gate-test.sh SIGNING_GATE "$signing_mutant" 'partial-require-'
else
	fail signing-gate-partial-bypass 'plant did not apply to scripts/signing-gate.sh'
fi

# Proves signing-gate-test.sh catches a gate that leaks values into xtrace:
# the mutant drops the tracing guard and copies each value into a traced
# assignment while keeping every exit code, so only the trace cases can fail.
leak_step="$SCRATCH/mutants/signing-gate-untraced.sh"
leak_mutant="$SCRATCH/mutants/signing-gate-leak.sh"
# shellcheck disable=SC2016 # literal $ in the planted lines
if plant_literal "$REPO_ROOT/scripts/signing-gate.sh" "$leak_step" \
	'{ set +x; } 2>/dev/null' ':' &&
	plant_literal "$leak_step" "$leak_mutant" \
		"$(printf '\teval "set_flag=\\${$name:+1}"')" \
		"$(printf '\teval "set_flag=\\${$name:+1} leak=\\${$name:-}"')"; then
	control signing-gate-trace-leak signing-gate-test.sh SIGNING_GATE "$leak_mutant" 'trace-exec-sh-full:'
else
	fail signing-gate-trace-leak 'plant did not apply to scripts/signing-gate.sh'
fi

# Proves cosign-sign-test.sh catches a wrapper that trusts cosign's exit
# code: the mutant skips the certificate check, so the help-effect case
# (cosign succeeds without signing) must fail against it.
cosign_mutant="$SCRATCH/mutants/cosign-sign-checksum.sh"
# shellcheck disable=SC2016 # literal $ in the planted lines
if plant_literal "$REPO_ROOT/scripts/cosign-sign-checksum.sh" "$cosign_mutant" \
	'[ -s "$certificate" ] || refuse 42 certificate_missing "cosign wrote no certificate $certificate"' \
	':'; then
	control cosign-sign-output-bypass cosign-sign-test.sh COSIGN_SIGN "$cosign_mutant" 'help-effect:'
else
	fail cosign-sign-output-bypass 'plant did not apply to scripts/cosign-sign-checksum.sh'
fi

# --- Lint narrowing controls ---------------------------------------------
# Each control copies the module into scratch, narrows one rule so it no
# longer reports its own ID (the call reports another registered ID with the
# same arguments), and requires the Go suite to FAIL naming that rule's
# driving test. The unmodified copy must pass first, so a failure comes from
# the narrowing and not from the copy. Go caches stay in the scratch root.
mkdir -p "$SCRATCH/go-build" "$SCRATCH/go-mod" "$SCRATCH/go-path"
export GOCACHE="$SCRATCH/go-build" GOMODCACHE="$SCRATCH/go-mod" GOPATH="$SCRATCH/go-path"
export GOFLAGS=-mod=readonly

# lint_copy DEST: the module, templates, scripts and workflows the Go tests
# read (TestSavedGateStepsExecute runs the saved steps against scripts/).
lint_copy() {
	mkdir -p "$1/.github"
	cp "$REPO_ROOT/go.mod" "$REPO_ROOT/go.sum" "$1/"
	cp -R "$REPO_ROOT/cmd" "$REPO_ROOT/template" "$REPO_ROOT/scripts" "$1/"
	cp -R "$REPO_ROOT/.github/workflows" "$1/.github/"
}

# Proves the copied suite passes unmodified (control for the controls below).
lint_copy "$SCRATCH/lint-base"
if (cd "$SCRATCH/lint-base" && go test -count=1 ./cmd/release-standard-lint/) >"$SCRATCH/lint-base.log" 2>&1; then
	pass lint-copy-baseline
else
	fail lint-copy-baseline "unmodified copy failed; log: $SCRATCH/lint-base.log"
fi

# module_mutant LABEL FILE OLD NEW TEST_NAME: in a module copy, replace the
# one occurrence of OLD in FILE (relative to the repository root) with NEW
# (which may span lines), and require the Go suite to fail naming TEST_NAME.
module_mutant() {
	local label=$1 rel=$2 old=$3 new=$4 test_name=$5 dir="$SCRATCH/mut-$1" rc=0 file count
	lint_copy "$dir"
	file="$dir/$rel"
	count=$(grep -Fc -- "$old" "$file" || true)
	if [ "$count" != 1 ]; then
		fail "$label" "plant matches $count lines of $rel, want 1"
		return
	fi
	OLD=$old NEW=$new awk '{ i = index($0, ENVIRON["OLD"]); if (i) $0 = substr($0, 1, i - 1) ENVIRON["NEW"] substr($0, i + length(ENVIRON["OLD"])); print }' \
		"$REPO_ROOT/$rel" >"$file"
	if cmp -s "$REPO_ROOT/$rel" "$file"; then
		fail "$label" "plant did not change $rel"
		return
	fi
	(cd "$dir" && go test -count=1 ./cmd/release-standard-lint/) >"$SCRATCH/$label.log" 2>&1 || rc=$?
	if [ "$rc" -ne 0 ] && grep -q -- "--- FAIL: $test_name" "$SCRATCH/$label.log"; then
		pass "$label"
	else
		fail "$label" "suite did not catch the mutant (rc=$rc); log: $SCRATCH/$label.log"
	fi
}

# lint_narrowing LABEL OLD_CALL NEW_CALL TEST_NAME: module_mutant on lint.go.
lint_narrowing() {
	module_mutant "$1" cmd/release-standard-lint/lint.go "$2" "$3" "$4"
}

# Proves the Go suite kills each of the three rules the r1 review found
# undriven, and the two new P1 rules, when narrowed away.
lint_narrowing lint-narrow-builds-missing 'f.add("builds-missing", ' 'f.add("config-version", ' TestBuildsAndNfpmsMissing
lint_narrowing lint-narrow-nfpm-missing 'f.add("nfpm-missing", ' 'f.add("config-version", ' TestBuildsAndNfpmsMissing
lint_narrowing lint-narrow-skip-upload 'f.add("skip-upload", ' 'f.add("cask-skip-upload", ' TestSkipUpload
lint_narrowing lint-narrow-hook-order 'f.add("signing-gate-hook-order", "before.hooks[0] must be %s; ' 'f.add("signing-gate-hook", "before.hooks[0] must be %s; ' TestSigningGateHookOrder
lint_narrowing lint-narrow-notarize-uncovered 'f.add("notarize-uncovered", "builds[%d]' 'f.add("notarize-missing", "builds[%d]' TestNotarizeCoverage

# Proves the r2 fixes are each load-bearing: the suite fails when later
# before hooks or build hooks are admitted again, when notarize ids are
# trimmed before matching, and when env entries are trimmed before checking.
lint_narrowing lint-narrow-later-before-hook 'f.add("caller-hook", "before.hooks[%d] must not be set' 'f.add("config-version", "before.hooks[%d] must not be set' TestCallerHooksRefused
lint_narrowing lint-narrow-build-hooks 'f.add("caller-hook", "%s.hooks must not be set' 'f.add("config-version", "%s.hooks must not be set' TestCallerHooksRefused
lint_narrowing lint-narrow-trimmed-ids 'selected := rawStringList(m["ids"])' 'selected := stringList(m["ids"])' TestNotarizeCoverage
lint_narrowing lint-narrow-trimmed-env 'name, value, _ := strings.Cut(s, "=")' 'name, value, _ := strings.Cut(strings.TrimSpace(s), "=")' TestEffectiveBuildSettings

# Proves the r3 credential rules are each load-bearing: the suite fails when
# the release target is no longer compared with the calling repository, when
# the cask token is no longer checked, when the env-reference rule admits any
# name, and when a bare . is no longer refused.
CRED=cmd/release-standard-lint/credentials.go
module_mutant cred-narrow-release-repo "$CRED" 'if opt.ExpectRepository != "" && !strings.EqualFold(' 'if false && !strings.EqualFold(' TestReleaseTargetRefused
module_mutant cred-narrow-tap-token "$CRED" 'if rawString(repo["token"]) != tapToken {' 'if false {' TestTapRepository
module_mutant cred-narrow-env-allow "$CRED" 'return allowed[name] || name == extra }' 'return allowed[name] || name == extra || name != "" }' TestTemplateEnvRefused
module_mutant cred-narrow-bare-dot "$CRED" 'add("a bare . exposes the whole template context, including Env")' '_ = 0' TestTemplateEnvRefused

# Proves the workflow contract kills a disabled or emptied authoritative gate
# (r2: if: false on the presence gate survived every suite): the review's
# if: false, continue-on-error on the post-GoReleaser tree check, and a
# token-preserving no-op body for the presence gate.
WF=.github/workflows/go-release.yml
module_mutant workflow-gate-if-false "$WF" '      - name: Signing presence gate' \
	"$(printf '%s\n%s' '      - name: Signing presence gate' '        if: false')" TestReusableWorkflowGatesBeforeGoReleaser
module_mutant workflow-post-check-continue "$WF" '      - name: Confirm GoReleaser left the standard scripts unmodified' \
	"$(printf '%s\n%s' '      - name: Confirm GoReleaser left the standard scripts unmodified' '        continue-on-error: true')" TestReusableWorkflowGatesBeforeGoReleaser
# shellcheck disable=SC2016 # literal workflow body
module_mutant workflow-gate-noop-body "$WF" '          "$GITHUB_WORKSPACE/release-standard/scripts/signing-gate.sh"' \
	'          : "$GITHUB_WORKSPACE/release-standard/scripts/signing-gate.sh"' TestSavedGateStepsExecute

# Proves tag-gate-test.sh catches an LF-only prohibited-format match (r2):
# the CRLF cases in optional mode must fail against that mutant.
crlf_mutant="$SCRATCH/mutants/tag-gate-lf-only.sh"
# shellcheck disable=SC2016 # literal $ in the planted lines
if plant_literal "$REPO_ROOT/scripts/tag-gate.sh" "$crlf_mutant" \
	"if printf '%s\n' \"\$body\" | grep -Eq -- '^-----BEGIN (PGP SIGNATURE|PGP MESSAGE|SIGNED MESSAGE)-----'; then" \
	"if printf '%s\n' \"\$body\" | grep -Eqx -- '-----BEGIN (PGP SIGNATURE|PGP MESSAGE|SIGNED MESSAGE)-----'; then"; then
	control tag-gate-crlf-armor tag-gate-test.sh TAG_GATE "$crlf_mutant" 'crlf-pgp-signature-opt-out:'
else
	fail tag-gate-crlf-armor 'plant did not apply to scripts/tag-gate.sh'
fi

finish harness-control-test
