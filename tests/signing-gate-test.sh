#!/usr/bin/env bash
# Signing-gate controls. Synthetic values only; the gate never signs and is
# never given a real secret. Every case runs with all five fields and
# RELEASE_REQUIRE_SIGNING cleared first, then sets exactly the case's fields.
# Each matrix case must print exactly one line, never the synthetic value,
# and exit with the documented code. The trace cases at the end run the gate
# under sh -x, bash -x and sourcing from a traced shell, and search the whole
# output for every per-field dummy. SIGNING_GATE overrides the gate under
# test; see harness-control-test.sh.
set -eu -o pipefail
# shellcheck source=tests/lib.sh
. "$(dirname "$0")/lib.sh"
GATE=${SIGNING_GATE:-$REPO_ROOT/scripts/signing-gate.sh}
make_scratch
LOG="$SCRATCH/signing.log"
VALUE='fixture-secret-not-for-logging'
FIELDS=(MACOS_SIGN_P12 MACOS_SIGN_P12_PASSWORD ASC_ISSUER_ID ASC_KEY_ID ASC_KEY_P8)

# signing_case LABEL WANT_RC WANT_LINE_PREFIX MODE [NAME=VALUE...]
# MODE is the RELEASE_REQUIRE_SIGNING value, or "unset".
signing_case() {
	local label=$1 want_rc=$2 want=$3 mode=$4
	shift 4
	local rc=0
	local -a envs=(-u RELEASE_REQUIRE_SIGNING)
	local field
	for field in "${FIELDS[@]}"; do
		envs+=(-u "$field")
	done
	if [ "$mode" != unset ]; then
		envs+=("RELEASE_REQUIRE_SIGNING=$mode")
	fi
	env "${envs[@]}" "$@" "$GATE" >"$LOG" 2>&1 || rc=$?
	if [ "$rc" -eq "$want_rc" ] &&
		[ "$(wc -l <"$LOG" | tr -d ' ')" = 1 ] &&
		grep -Fq -- "$want" "$LOG" &&
		[ "$(head -c "${#want}" "$LOG")" = "$want" ] &&
		! grep -Fq -- "$VALUE" "$LOG"; then
		pass "$label"
	else
		fail "$label" "want rc=$want_rc '$want...', got rc=$rc: $(cat "$LOG")"
	fi
}

UNSIGNED='signing-gate: unsigned (no signing fields; require_signing=false)'
SIGNED='signing-gate: signed (all five signing fields present)'
REQUIRED='signing-gate: REFUSED[signing_required_but_absent]: '
PARTIAL='signing-gate: REFUSED[partial_signing_config]: '

# Proves the full presence matrix in both modes: 0/5 builds unsigned only
# with require_signing=false and refuses with true; 5/5 is signed in both;
# every one of the 30 partial subsets (all counts 1-4) refuses in both modes
# and reports its count. Mask bit i sets FIELDS[i].
for mode in false true; do
	mask=0
	while [ "$mask" -lt 32 ]; do
		assign=()
		count=0
		i=0
		while [ "$i" -lt 5 ]; do
			if [ $((mask & (1 << i))) -ne 0 ]; then
				if [ "${FIELDS[$i]}" = ASC_KEY_P8 ]; then
					# A multi-line PEM-like value still counts as one field.
					assign+=("ASC_KEY_P8=$(printf '%s\nsecond-line' "$VALUE")")
				else
					assign+=("${FIELDS[$i]}=$VALUE")
				fi
				count=$((count + 1))
			fi
			i=$((i + 1))
		done
		label="require-$mode-mask-$mask"
		case $count in
		0)
			if [ "$mode" = false ]; then
				signing_case "$label" 0 "$UNSIGNED" "$mode"
			else
				signing_case "$label" 4 "$REQUIRED" "$mode"
			fi
			;;
		5) signing_case "$label" 0 "$SIGNED" "$mode" ${assign[@]+"${assign[@]}"} ;;
		*) signing_case "partial-$label" 3 "${PARTIAL}$count of 5" "$mode" ${assign[@]+"${assign[@]}"} ;;
		esac
		mask=$((mask + 1))
	done
done

# Proves the default is fail-closed: with RELEASE_REQUIRE_SIGNING unset, 0/5
# refuses and 5/5 is signed.
signing_case default-none 4 "$REQUIRED" unset
signing_case default-all 0 "$SIGNED" unset \
	"MACOS_SIGN_P12=$VALUE" "MACOS_SIGN_P12_PASSWORD=$VALUE" \
	"ASC_ISSUER_ID=$VALUE" "ASC_KEY_ID=$VALUE" "ASC_KEY_P8=$VALUE"

# Proves empty values count as absent (GitHub expands an unset secret to an
# empty string): five empty fields equal 0/5 in both modes, and four real
# fields plus one empty field are partial.
signing_case empty-false 0 "$UNSIGNED" false \
	MACOS_SIGN_P12= MACOS_SIGN_P12_PASSWORD= ASC_ISSUER_ID= ASC_KEY_ID= ASC_KEY_P8=
signing_case empty-true 4 "$REQUIRED" true \
	MACOS_SIGN_P12= MACOS_SIGN_P12_PASSWORD= ASC_ISSUER_ID= ASC_KEY_ID= ASC_KEY_P8=
signing_case empty-one 3 "${PARTIAL}4 of 5" true \
	"MACOS_SIGN_P12=$VALUE" "MACOS_SIGN_P12_PASSWORD=$VALUE" \
	"ASC_ISSUER_ID=$VALUE" "ASC_KEY_ID=$VALUE" ASC_KEY_P8=

# Proves an invalid mode is refused by name even with all five fields set,
# next to the same fields accepted under a valid mode.
signing_case invalid-mode 2 'signing-gate: REFUSED[invalid_require_signing]: ' yes \
	"MACOS_SIGN_P12=$VALUE" "MACOS_SIGN_P12_PASSWORD=$VALUE" \
	"ASC_ISSUER_ID=$VALUE" "ASC_KEY_ID=$VALUE" "ASC_KEY_P8=$VALUE"
signing_case invalid-mode-control 0 "$SIGNED" true \
	"MACOS_SIGN_P12=$VALUE" "MACOS_SIGN_P12_PASSWORD=$VALUE" \
	"ASC_ISSUER_ID=$VALUE" "ASC_KEY_ID=$VALUE" "ASC_KEY_P8=$VALUE"

# --- Trace secrecy -----------------------------------------------------------
# Every field gets its own dummy, so a leak names the field it came from.
# Multiline dummies put a distinct token on each line, and one dummy carries
# command-substitution syntax that must never be evaluated.
dummy() { printf 'DUMMY-%s-trace-7f3a' "$1"; }
dummy_multi() { printf 'HEAD-%s-line-9c1e\nDUMMY-%s-tail-9c1e' "$1" "$1"; }
PWNED="$SCRATCH/pwned"
METACHAR="\$(touch $PWNED)\`touch $PWNED\`DUMMY-metachar-5b2d"
TRACE_LOG="$SCRATCH/trace.log"

# Runs the gate (or $PROBE) under one tracing runner. Exec runners pass -x on
# the command line; source runners enable set -x and then source the file.
# run_traced RUNNER SCRIPT [env arguments...]
# shellcheck disable=SC2016 # $1 belongs to the inner shell
run_traced() {
	local runner=$1 script=$2
	shift 2
	case $runner in
	exec-sh) env "$@" sh -x "$script" ;;
	exec-bash) env "$@" bash -x "$script" ;;
	source-sh) env "$@" sh -c 'set -x; . "$1"' sh "$script" ;;
	source-bash) env "$@" bash -c 'set -x; . "$1"' bash "$script" ;;
	esac
}

# leaked_tokens: prints every dummy token found in $TRACE_LOG (none expected).
leaked_tokens() {
	local field token
	for field in "${FIELDS[@]}"; do
		for token in "DUMMY-$field-trace-7f3a" "HEAD-$field-line-9c1e" "DUMMY-$field-tail-9c1e"; do
			if grep -Fq -- "$token" "$TRACE_LOG"; then
				printf ' %s' "$token"
			fi
		done
	done
	if grep -Fq -- 'DUMMY-metachar-5b2d' "$TRACE_LOG"; then
		printf ' DUMMY-metachar-5b2d'
	fi
}

# trace_case LABEL RUNNER WANT_RC WANT_LINE_PREFIX MODE [NAME=VALUE...]
trace_case() {
	local label=$1 runner=$2 want_rc=$3 want=$4 mode=$5
	shift 5
	local rc=0 leaks
	local -a envs=(-u RELEASE_REQUIRE_SIGNING -u PS4 -u ENV -u BASH_ENV)
	local field
	for field in "${FIELDS[@]}"; do
		envs+=(-u "$field")
	done
	if [ "$mode" != unset ]; then
		envs+=("RELEASE_REQUIRE_SIGNING=$mode")
	fi
	rm -f "$PWNED"
	run_traced "$runner" "$GATE" "${envs[@]}" "$@" >"$TRACE_LOG" 2>&1 || rc=$?
	leaks=$(leaked_tokens)
	if [ "$rc" -eq "$want_rc" ] && [ -z "$leaks" ] && [ ! -e "$PWNED" ] &&
		grep -Fq -- "$want" "$TRACE_LOG"; then
		pass "$label"
	else
		fail "$label" "want rc=$want_rc '$want' and no dummy, got rc=$rc leaks:[${leaks}] pwned:$([ -e "$PWNED" ] && echo yes || echo no): $(head -c 400 "$TRACE_LOG")"
	fi
}

# Proves each tracing runner really traces and that the capture and leak
# search see what it prints: a probe that expands a dummy in a traced command
# must show the dummy. Without this control a runner that silently does not
# trace would make every trace case below pass vacuously.
PROBE="$SCRATCH/trace-probe.sh"
# shellcheck disable=SC2016 # the probe expands the variable, not this shell
printf '%s\n' '#!/bin/sh' ': "$MACOS_SIGN_P12"' >"$PROBE"
chmod +x "$PROBE"
for runner in exec-sh exec-bash source-sh source-bash; do
	rc=0
	run_traced "$runner" "$PROBE" "MACOS_SIGN_P12=$(dummy MACOS_SIGN_P12)" >"$TRACE_LOG" 2>&1 || rc=$?
	if [ "$rc" -eq 0 ] && [ -n "$(leaked_tokens)" ]; then
		pass "trace-runner-control-$runner"
	else
		fail "trace-runner-control-$runner" "probe dummy not visible in trace (rc=$rc): $(head -c 400 "$TRACE_LOG")"
	fi
done

# Proves no field value reaches the output of a traced run, for every runner
# (sh -x, bash -x, and sourcing from a traced sh or bash), on the full-five
# success path with single-line and multiline dummies, on each single-field
# and each four-field partial path, on the invalid-mode and required-absent
# error paths, and with a command-substitution dummy that must not run. Each
# case also requires the documented exit code and result line, so a crash is
# not mistaken for a clean run. Limit: covers these shells and paths, not
# every shell or PS4 configuration.
for runner in exec-sh exec-bash source-sh source-bash; do
	all_single=()
	all_multi=()
	for field in "${FIELDS[@]}"; do
		all_single+=("$field=$(dummy "$field")")
		all_multi+=("$field=$(dummy_multi "$field")")
	done
	trace_case "trace-$runner-full" "$runner" 0 "$SIGNED" true "${all_single[@]}"
	trace_case "trace-$runner-full-multiline" "$runner" 0 "$SIGNED" true "${all_multi[@]}"
	trace_case "trace-$runner-full-metachar" "$runner" 0 "$SIGNED" false \
		"MACOS_SIGN_P12=$METACHAR" "${all_single[@]:1}"
	for i in 0 1 2 3 4; do
		field=${FIELDS[$i]}
		trace_case "trace-$runner-only-$field" "$runner" 3 "${PARTIAL}1 of 5" true "${all_single[$i]}"
		trace_case "trace-$runner-only-$field-multiline" "$runner" 3 "${PARTIAL}1 of 5" false "${all_multi[$i]}"
		four=()
		for j in 0 1 2 3 4; do
			[ "$j" -eq "$i" ] || four+=("${all_single[$j]}")
		done
		trace_case "trace-$runner-without-$field" "$runner" 3 "${PARTIAL}4 of 5" true "${four[@]}"
	done
	trace_case "trace-$runner-invalid-mode" "$runner" 2 'signing-gate: REFUSED[invalid_require_signing]: ' yes "${all_multi[@]}"
	trace_case "trace-$runner-required-absent" "$runner" 4 "$REQUIRED" true
done

finish signing-gate-test
