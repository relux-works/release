#!/bin/sh
{ set +x; } 2>/dev/null
# Keyless cosign signature over checksums.txt for the Relux release standard.
# The template's signs entry runs it as `sh <this> ${artifact} ${certificate}
# ${signature}`. It owns the cosign arguments, so a caller config cannot turn
# signing into a no-op (for example sign-blob --help), and it refuses unless
# cosign produced both verification files in this run. GoReleaser runs signs
# before publishing, so a refusal here stops the release before any upload.
#
#   cosign-sign-checksum.sh ARTIFACT ARTIFACT.pem ARTIFACT.sig
#
#   exit 0  both files written by cosign in this run
#   exit 2  usage                  bad arguments or output names
#   exit 40 artifact_missing       ARTIFACT is not a nonempty regular file
#   exit 41 cosign_failed          cosign exited nonzero
#   exit 42 certificate_missing    cosign left no nonempty certificate
#   exit 43 signature_missing      cosign left no nonempty signature
set -eu

me='cosign-sign-checksum'

refuse() {
	printf '%s: REFUSED[%s]: %s\n' "$me" "$2" "$3" >&2
	exit "$1"
}

[ $# -eq 3 ] || refuse 2 usage 'usage: cosign-sign-checksum.sh ARTIFACT ARTIFACT.pem ARTIFACT.sig'
artifact=$1
certificate=$2
signature=$3
[ "$certificate" = "$artifact.pem" ] || refuse 2 usage "certificate must be $artifact.pem"
[ "$signature" = "$artifact.sig" ] || refuse 2 usage "signature must be $artifact.sig"
{ [ -f "$artifact" ] && [ -s "$artifact" ]; } || refuse 40 artifact_missing "$artifact is not a nonempty regular file"

# Outputs left by an earlier run must not satisfy the checks below.
rm -f -- "$certificate" "$signature"
cosign sign-blob --yes \
	--output-certificate="$certificate" \
	--output-signature="$signature" \
	"$artifact" || refuse 41 cosign_failed "cosign sign-blob failed for $artifact"
[ -s "$certificate" ] || refuse 42 certificate_missing "cosign wrote no certificate $certificate"
[ -s "$signature" ] || refuse 43 signature_missing "cosign wrote no signature $signature"
printf '%s: signed %s (%s, %s)\n' "$me" "$artifact" "$certificate" "$signature"
