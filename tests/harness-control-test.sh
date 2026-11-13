#!/usr/bin/env bash
# Instrument controls: proves the gate harnesses are not blind. Each control
# copies a gate into scratch (the tracked scripts are never edited), plants a
# bypass, confirms the plant applied, reruns the full harness against the
# mutant and requires the harness to FAIL on the exact case the bypass
# defeats. A plant that did not apply, or a harness that passes, fails here.
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

finish harness-control-test
