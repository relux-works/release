#!/usr/bin/env bash
# cosign-sign-checksum.sh controls with a fake cosign on PATH. No real
# cosign, key, network or Sigstore call: the fake only records its arguments
# and writes (or does not write) the two output files. COSIGN_SIGN overrides
# the wrapper under test; see harness-control-test.sh.
set -eu -o pipefail
# shellcheck source=tests/lib.sh
. "$(dirname "$0")/lib.sh"
WRAPPER=${COSIGN_SIGN:-$REPO_ROOT/scripts/cosign-sign-checksum.sh}
make_scratch
DIST="$SCRATCH/work/dist"
mkdir -p "$DIST" "$SCRATCH/fakebin"
ARTIFACT="$DIST/checksums.txt"
printf 'abc  relux-fixture_1.2.3_linux_amd64.tar.gz\n' >"$ARTIFACT"
ARGS_LOG="$SCRATCH/cosign-args.log"

# Fake cosign. FAKE_COSIGN_MODE: ok writes both outputs; help prints usage
# and exits 0 like `sign-blob --help`; sig-only and cert-only write one file;
# fail exits 1.
cat >"$SCRATCH/fakebin/cosign" <<'FAKE'
#!/bin/sh
printf '%s\n' "$@" >"$FAKE_COSIGN_ARGS"
cert= sig=
for a in "$@"; do
	case $a in
	--output-certificate=*) cert=${a#--output-certificate=} ;;
	--output-signature=*) sig=${a#--output-signature=} ;;
	esac
done
case $FAKE_COSIGN_MODE in
ok) printf 'fake-cert\n' >"$cert" && printf 'fake-sig\n' >"$sig" ;;
help) echo 'Usage: cosign sign-blob [flags]' ;;
sig-only) printf 'fake-sig\n' >"$sig" ;;
cert-only) printf 'fake-cert\n' >"$cert" ;;
fail) exit 1 ;;
esac
FAKE
chmod +x "$SCRATCH/fakebin/cosign"

# wrap_case LABEL MODE WANT_RC WANT_TEXT [ARGS...] (default args: the standard three)
wrap_case() {
	local label=$1 mode=$2 want_rc=$3 want=$4 rc=0
	shift 4
	[ $# -gt 0 ] || set -- "$ARTIFACT" "$ARTIFACT.pem" "$ARTIFACT.sig"
	rm -f "$ARGS_LOG"
	PATH="$SCRATCH/fakebin:$PATH" FAKE_COSIGN_MODE=$mode FAKE_COSIGN_ARGS=$ARGS_LOG \
		sh "$WRAPPER" "$@" >"$SCRATCH/out.log" 2>&1 || rc=$?
	if [ "$rc" -eq "$want_rc" ] && grep -Fq -- "$want" "$SCRATCH/out.log"; then
		pass "$label"
	else
		fail "$label" "want rc=$want_rc '$want', got rc=$rc: $(cat "$SCRATCH/out.log")"
	fi
}

# Proves the positive: cosign writing both files passes, the wrapper calls
# exactly `cosign sign-blob --yes --output-certificate=<pem>
# --output-signature=<sig> <artifact>`, and both files exist afterwards.
wrap_case signed ok 0 'cosign-sign-checksum: signed '
expected_args=$(printf '%s\n' sign-blob --yes "--output-certificate=$ARTIFACT.pem" "--output-signature=$ARTIFACT.sig" "$ARTIFACT")
if [ -f "$ARGS_LOG" ] && [ "$(cat "$ARGS_LOG")" = "$expected_args" ] && [ -s "$ARTIFACT.pem" ] && [ -s "$ARTIFACT.sig" ]; then
	pass signed-arguments-and-outputs
else
	fail signed-arguments-and-outputs "args: $(cat "$ARGS_LOG" 2>/dev/null)"
fi

# Proves the command-effect regression: a cosign run that succeeds without
# signing (the effect of sign-blob --help) is refused, also when outputs from
# an earlier run are still on disk, because the wrapper removes them first.
wrap_case help-effect help 42 'REFUSED[certificate_missing]'
printf 'stale\n' >"$ARTIFACT.pem"
printf 'stale\n' >"$ARTIFACT.sig"
wrap_case help-effect-stale-outputs help 42 'REFUSED[certificate_missing]'

# Proves each missing output and a failing cosign are refused by name.
wrap_case signature-only sig-only 42 'REFUSED[certificate_missing]'
wrap_case certificate-only cert-only 43 'REFUSED[signature_missing]'
wrap_case cosign-fails fail 41 'REFUSED[cosign_failed]'

# Proves bad arguments are refused before cosign runs: wrong output names,
# a missing or empty artifact, and a wrong argument count.
wrap_case other-certificate ok 2 'REFUSED[usage]' "$ARTIFACT" "$ARTIFACT.crt" "$ARTIFACT.sig"
wrap_case other-signature ok 2 'REFUSED[usage]' "$ARTIFACT" "$ARTIFACT.pem" "$ARTIFACT.asc"
wrap_case missing-artifact ok 40 'REFUSED[artifact_missing]' "$DIST/absent.txt" "$DIST/absent.txt.pem" "$DIST/absent.txt.sig"
: >"$DIST/empty.txt"
wrap_case empty-artifact ok 40 'REFUSED[artifact_missing]' "$DIST/empty.txt" "$DIST/empty.txt.pem" "$DIST/empty.txt.sig"
wrap_case argument-count ok 2 'REFUSED[usage]' "$ARTIFACT"
if [ ! -e "$ARGS_LOG" ]; then
	pass refused-before-cosign
else
	fail refused-before-cosign 'cosign ran for a refused argument list'
fi
wrap_case signed-again ok 0 'cosign-sign-checksum: signed '

finish cosign-sign-test
