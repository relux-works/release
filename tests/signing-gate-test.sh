#!/usr/bin/env bash
# Signing-gate controls. Synthetic values only; the gate never signs and is
# never given a real secret. Every case runs with all five fields and
# RELEASE_REQUIRE_SIGNING cleared first, then sets exactly the case's fields.
# Each case must print exactly one line, never the synthetic value, and exit
# with the documented code. SIGNING_GATE overrides the gate under test; see
# harness-control-test.sh.
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

finish signing-gate-test
