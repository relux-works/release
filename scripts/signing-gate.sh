#!/bin/sh
# Darwin signing presence gate for the Relux release standard.
#
# Runs as the GoReleaser `before` hook, so it sees exactly the environment the
# GoReleaser step sees. Presence only: it never prints a value, signs anything
# or contacts Apple. Empty values count as absent, matching GoReleaser's
# isEnvSet predicate and GitHub's empty expansion of an unset secret.
#
#   RELEASE_REQUIRE_SIGNING  true (default) or false
#
#   fields present | require=false        | require=true
#   0 of 5         | exit 0, unsigned     | exit 4 signing_required_but_absent
#   1-4 of 5       | exit 3 partial_signing_config (always)
#   5 of 5         | exit 0, signed       | exit 0, signed
#   bad RELEASE_REQUIRE_SIGNING value: exit 2 invalid_require_signing
set -eu

me='signing-gate'

refuse() {
	# $1 exit code, $2 reason, $3 detail
	printf '%s: REFUSED[%s]: %s\n' "$me" "$2" "$3" >&2
	exit "$1"
}

require=${RELEASE_REQUIRE_SIGNING:-true}
case $require in
true | false) ;;
*) refuse 2 invalid_require_signing 'RELEASE_REQUIRE_SIGNING must be true or false' ;;
esac

present=0
missing=
for name in MACOS_SIGN_P12 MACOS_SIGN_P12_PASSWORD ASC_ISSUER_ID ASC_KEY_ID ASC_KEY_P8; do
	# Fixed names only; the value is tested for emptiness and never printed.
	eval "value=\${$name:-}"
	if [ -n "$value" ]; then
		present=$((present + 1))
	else
		missing="$missing $name"
	fi
done
value=

case $present in
0)
	[ "$require" = false ] ||
		refuse 4 signing_required_but_absent 'require_signing=true but none of the five signing fields is set'
	printf '%s: unsigned (no signing fields; require_signing=false)\n' "$me"
	;;
5)
	printf '%s: signed (all five signing fields present)\n' "$me"
	;;
*)
	refuse 3 partial_signing_config "$present of 5 signing fields present; set all five or none; missing:$missing"
	;;
esac
