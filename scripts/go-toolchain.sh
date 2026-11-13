#!/bin/sh
# GoReleaser's Go tool for the Relux release standard (builds.tool).
# Pins GOTOOLCHAIN to the `go` line of go.mod in the working directory, as
# curator-agent-launcher's scripts/release-go.sh does, so a release build uses
# the toolchain the module declares even outside Actions.
set -eu
version=$(sed -n 's/^go //p' go.mod 2>/dev/null || true)
toolchain=go$version
lines=$(printf '%s\n' "$toolchain" | wc -l | tr -d ' ')
if [ "$lines" != 1 ] || ! printf '%s\n' "$toolchain" | grep -Eqx 'go[0-9]+\.[0-9]+(\.[0-9]+)?'; then
	echo 'go-toolchain: REFUSED[go_line_unreadable]: cannot read exactly one go line from ./go.mod' >&2
	exit 2
fi
export GOTOOLCHAIN="$toolchain"
exec go "$@"
