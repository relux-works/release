#!/bin/sh
# Tracing goes off before anything else, so no field can reach an xtrace log,
# also when this file is run with sh -x or bash -x or sourced from a traced
# shell. The brace group hides the trace line of this very command.
{ set +x; } 2>/dev/null
# Darwin signing presence gate for the Relux release standard.
#
# go-release.yml runs it as its own step before GoReleaser, from the verified
# standard checkout, with values from workflow inputs and secrets only. The
# GoReleaser `before` hook runs it again as defense in depth. Presence only:
# it never prints or copies a value, signs anything or contacts Apple. Each
# field is tested with ${NAME:+1}, which yields 1 or nothing and never the
# value. Empty values count as absent, matching GoReleaser's isEnvSet
# predicate and GitHub's empty expansion of an unset secret.
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
set_flag=
for name in MACOS_SIGN_P12 MACOS_SIGN_P12_PASSWORD ASC_ISSUER_ID ASC_KEY_ID ASC_KEY_P8; do
	# Fixed names only. set_flag is 1 or empty; the value is never expanded.
	eval "set_flag=\${$name:+1}"
	if [ "$set_flag" = 1 ]; then
		present=$((present + 1))
	else
		missing="$missing $name"
	fi
done

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
