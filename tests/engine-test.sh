#!/usr/bin/env bash
# Real-engine controls for the signing admission: runs the pinned GoReleaser
# (v2.17.0) on a throwaway caller fixture built from the template, with no
# signing field and no publication (`build --snapshot`). GORELEASER names the
# binary (default: goreleaser on PATH). Without it the test prints SKIP and
# exits 0, unless REQUIRE_ENGINE=1 (hosted CI), where a missing or other
# engine version fails. The tracked scripts are never run by the engine: it
# gets a scratch git checkout holding a copy of scripts/, standing in for the
# workflow's release-standard checkout, which the hook routes below
# overwrite. jq reads dist/artifacts.json for the artifact assertions.
set -eu -o pipefail
# shellcheck source=tests/lib.sh
. "$(dirname "$0")/lib.sh"
GORELEASER=${GORELEASER:-$(command -v goreleaser || true)}
if [ -z "$GORELEASER" ]; then
	if [ "${REQUIRE_ENGINE:-0}" = 1 ]; then
		echo 'FAIL engine-available: REQUIRE_ENGINE=1 but no goreleaser binary' >&2
		exit 1
	fi
	echo 'SKIP engine-test: goreleaser not found (set GORELEASER or REQUIRE_ENGINE=1)'
	exit 0
fi
make_scratch
mkdir -p "$SCRATCH/go-build" "$SCRATCH/go-mod" "$SCRATCH/go-path" "$SCRATCH/bin" "$SCRATCH/configs"
export GOCACHE="$SCRATCH/go-build" GOMODCACHE="$SCRATCH/go-mod" GOPATH="$SCRATCH/go-path"
export GOFLAGS=-mod=readonly

# Proves the engine under test is the version go-release.yml pins.
if "$GORELEASER" --version 2>&1 | grep -Eq '(^|[^0-9.])2\.17\.0([^0-9]|$)'; then
	pass engine-version
else
	fail engine-version "want GoReleaser 2.17.0, got: $("$GORELEASER" --version 2>&1 | head -5)"
fi

(cd "$REPO_ROOT" && go build -o "$SCRATCH/bin/lint" ./cmd/release-standard-lint)
LINT="$SCRATCH/bin/lint"
GATE="$REPO_ROOT/scripts/signing-gate.sh"
STANDARD="$SCRATCH/standard"
SCRIPTS="$STANDARD/scripts"
CHECK="$REPO_ROOT/scripts/standard-checkout-check.sh"
HOST_GOOS=$(go env GOOS)
HOST_GOARCH=$(go env GOARCH)
if command -v jq >/dev/null 2>&1; then
	pass jq-available
else
	fail jq-available 'jq is required for the artifact assertions'
fi

# Caller fixture: a minimal main package at the template's layout.
FIX="$SCRATCH/work/caller"
mkdir -p "$FIX"
cd "$FIX"
git init -q -b main
git config user.name 'Engine fixture'
git config user.email 'engine-fixture@example.invalid'
git config commit.gpgsign false
go_version=$(go env GOVERSION)
printf 'module example.invalid/relux-fixture\n\ngo %s\n' "${go_version#go}" >go.mod
printf 'package main\n\nvar version = "dev"\n\nfunc main() { println(version) }\n' >main.go
printf 'Fixture license\n' >LICENSE
printf 'Fixture notice\n' >NOTICE
printf 'Fixture readme\n' >README.md
printf 'dist/\n' >.gitignore
# The review's replacement wrapper: cosign sign-blob --help leaves no files.
printf '#!/bin/sh\nexec cosign sign-blob --help\n' >caller-cosign.sh
chmod +x caller-cosign.sh
git add .
git commit -q -m 'Engine fixture'

VALID="$SCRATCH/configs/valid.yml"
sed -e 's|__BINARY__|relux-fixture|g' -e 's|__MAIN_PACKAGE__|.|g' \
	-e 's|__VERSION_SYMBOL__|main.version|g' -e 's|__HOMEPAGE__|https://example.invalid/relux-fixture|g' \
	-e 's|__MAINTAINER__|Fixture <fixture@example.invalid>|g' -e 's|__DESCRIPTION__|Engine fixture|g' \
	-e 's|__LICENSE__|MIT|g' -e 's|__REPOSITORY_NAME__|relux-fixture|g' \
	"$REPO_ROOT/template/.goreleaser.yml" >"$VALID"

# variant NAME OLD NEW: writes configs/NAME.yml from the valid config with
# exactly one literal replacement, or fails the plant.
variant() {
	local count
	count=$(grep -Fc -- "$2" "$VALID" || true)
	[ "$count" = 1 ] || {
		fail "plant:$1" "matches $count lines, want 1"
		return 1
	}
	OLD=$2 NEW=$3 awk '{ i = index($0, ENVIRON["OLD"]); if (i) $0 = substr($0, 1, i - 1) ENVIRON["NEW"] substr($0, i + length(ENVIRON["OLD"])); print }' \
		"$VALID" >"$SCRATCH/configs/$1.yml"
}

# fresh_standard: a new git checkout of a copy of scripts/ at $STANDARD,
# with its commit in STANDARD_SHA, as the workflow's checkout would be.
fresh_standard() {
	rm -rf "$STANDARD"
	mkdir -p "$STANDARD"
	cp -R "$REPO_ROOT/scripts" "$SCRIPTS"
	git -C "$STANDARD" init -q -b main
	git -C "$STANDARD" add .
	git -C "$STANDARD" -c user.name='Standard fixture' -c user.email='standard-fixture@example.invalid' \
		-c commit.gpgsign=false commit -q -m 'Standard fixture'
	STANDARD_SHA=$(git -C "$STANDARD" rev-parse HEAD)
}

# engine CONFIG REQUIRE [GORELEASER_ARGS...]: fresh standard checkout, then
# a snapshot build (default: every target, Darwin and Linux) with no signing
# field. Output in $SCRATCH/engine.log; returns GoReleaser's rc.
engine() {
	local config=$1 require=$2
	shift 2
	fresh_standard
	env -u MACOS_SIGN_P12 -u MACOS_SIGN_P12_PASSWORD -u ASC_ISSUER_ID -u ASC_KEY_ID -u ASC_KEY_P8 \
		-u TAP_GITHUB_TOKEN -u GITHUB_TOKEN \
		RELEASE_STANDARD_SCRIPTS="$SCRIPTS" RELEASE_REQUIRE_SIGNING="$require" \
		"$GORELEASER" build --snapshot --clean --config "$config" "$@" >"$SCRATCH/engine.log" 2>&1
}

# post_check: the workflow's after-GoReleaser tree check on the standard
# checkout; returns its rc (0 unchanged, 31 modified).
post_check() {
	"$CHECK" "$STANDARD" "$STANDARD_SHA" >"$SCRATCH/post.log" 2>&1
}

# darwin_selected ID: how many Darwin binaries of the last engine run carry
# exactly this build id, which is what a notarize entry with ids [ID] selects.
darwin_selected() {
	jq --arg id "$1" '[.[] | select(.type == "Binary" and .goos == "darwin" and .extra.ID == $id)] | length' \
		"$FIX/dist/artifacts.json"
}

# host_binary: path of the last engine run's binary for the host target.
host_binary() {
	jq -r --arg os "$HOST_GOOS" --arg arch "$HOST_GOARCH" \
		'[.[] | select(.type == "Binary" and .goos == $os and .goarch == $arch)][0].path' \
		"$FIX/dist/artifacts.json"
}

# build_setting KEY: the value `go version -m` records for KEY in the host binary.
build_setting() {
	local bin
	bin=$(host_binary)
	case $bin in /*) ;; *) bin="$FIX/$bin" ;; esac
	go version -m "$bin" | awk -v key="$1" '$1 == "build" { split($2, kv, "="); if (kv[1] == key) { print substr($2, length(key) + 2); exit } }'
}

# workflow_gate REQUIRE: the reusable workflow's own presence-gate step, run
# as go-release.yml runs it (inputs and secrets only, no caller config).
workflow_gate() {
	env -u MACOS_SIGN_P12 -u MACOS_SIGN_P12_PASSWORD -u ASC_ISSUER_ID -u ASC_KEY_ID -u ASC_KEY_P8 \
		RELEASE_REQUIRE_SIGNING="$1" "$GATE" >"$SCRATCH/gate.log" 2>&1
}

expect_lint() {
	local label=$1 want=$2 config=$3 rc=0
	"$LINT" -main . -binary relux-fixture "$config" >"$SCRATCH/lint.out" 2>&1 || rc=$?
	if { [ -z "$want" ] && [ "$rc" -eq 0 ]; } || { [ -n "$want" ] && [ "$rc" -eq 1 ] && grep -Fq -- ": $want: " "$SCRATCH/lint.out"; }; then
		pass "$label"
	else
		fail "$label" "want lint ${want:-ok}, got rc=$rc: $(cat "$SCRATCH/lint.out")"
	fi
}

# Proves the positive controls: the filled template passes the lint without
# placeholders, the engine builds it unsigned with require_signing=false, and
# the in-engine hook refuses it with require_signing=true and no field. So
# the engine really runs the gate hook and the fixture builds.
expect_lint valid-lint '' "$VALID"
rc=0
engine "$VALID" false || rc=$?
if [ "$rc" -eq 0 ]; then pass engine-valid-unsigned; else fail engine-valid-unsigned "rc=$rc: $(tail -20 "$SCRATCH/engine.log")"; fi

# Proves, on the valid build: the standard checkout is unchanged after the
# engine (the after-GoReleaser check passes, so its refusals below come from
# the planted hooks); the notarize ids select a nonempty set of Darwin
# binaries; and the host binary records CGO_ENABLED=0, -trimpath=true and no
# VCS stamp, read from the binary with go version -m.
rc=0
post_check || rc=$?
if [ "$rc" -eq 0 ]; then pass engine-valid-post-check; else fail engine-valid-post-check "rc=$rc: $(cat "$SCRATCH/post.log")"; fi
selected=$(darwin_selected relux-fixture || echo error)
if [ "$selected" != error ] && [ "$selected" -ge 1 ]; then
	pass engine-valid-darwin-selected
else
	fail engine-valid-darwin-selected "notarize ids select $selected Darwin binaries"
fi
cgo=$(build_setting CGO_ENABLED || true)
trimpath=$(build_setting -trimpath || true)
vcs=$(build_setting vcs.revision || true)
if [ "$cgo" = 0 ] && [ "$trimpath" = true ] && [ -z "$vcs" ]; then
	pass engine-valid-build-metadata
else
	fail engine-valid-build-metadata "CGO_ENABLED='$cgo' -trimpath='$trimpath' vcs.revision='$vcs'"
fi
rc=0
engine "$VALID" true || rc=$?
if [ "$rc" -ne 0 ] && grep -Fq 'signing_required_but_absent' "$SCRATCH/engine.log"; then
	pass engine-valid-required-absent
else
	fail engine-valid-required-absent "rc=$rc: $(tail -20 "$SCRATCH/engine.log")"
fi

# route LABEL CONFIG LINT_FINDING: the bypass is live in the engine (snapshot
# succeeds with require_signing=true and no field), the lint refuses the
# config by name, and the workflow's presence gate, which runs before
# GoReleaser and never reads the config, refuses with exit 4.
route() {
	local label=$1 config=$2 finding=$3 rc=0
	engine "$config" true || rc=$?
	if [ "$rc" -eq 0 ]; then
		pass "$label-engine-route-live"
	else
		fail "$label-engine-route-live" "the planted route no longer bypasses the in-engine hook (rc=$rc); re-check this control against the engine: $(tail -20 "$SCRATCH/engine.log")"
	fi
	expect_lint "$label-lint" "$finding" "$config"
	rc=0
	workflow_gate true || rc=$?
	if [ "$rc" -eq 4 ] && grep -Fq 'REFUSED[signing_required_but_absent]' "$SCRATCH/gate.log"; then
		pass "$label-workflow-gate"
	else
		fail "$label-workflow-gate" "rc=$rc: $(cat "$SCRATCH/gate.log")"
	fi
}

# Proves the env route (review r1): caller root env overriding the policy.
if variant env-policy 'version: 2' "$(printf 'version: 2\nenv:\n  - RELEASE_REQUIRE_SIGNING=false')"; then
	route env-policy "$SCRATCH/configs/env-policy.yml" protected-env
fi

# Proves the scripts-path route: caller root env pointing the hook at a
# replacement gate that always succeeds.
FAKE="$SCRATCH/fake-scripts"
mkdir -p "$FAKE"
printf '#!/bin/sh\nexit 0\n' >"$FAKE/signing-gate.sh"
cp "$REPO_ROOT/scripts/go-toolchain.sh" "$FAKE/go-toolchain.sh"
chmod +x "$FAKE/signing-gate.sh" "$FAKE/go-toolchain.sh"
if variant env-scripts 'version: 2' "$(printf 'version: 2\nenv:\n  - RELEASE_STANDARD_SCRIPTS=%s' "$FAKE")"; then
	route env-scripts "$SCRATCH/configs/env-scripts.yml" protected-env
fi

# Proves the earlier-hook route: a before hook ahead of the gate that rewrites
# the shared gate (the scratch copy) into one that always succeeds.
# shellcheck disable=SC2016 # literal template and printf text for the config
hook_line=$(printf "    - sh -c 'printf \"#!/bin/sh\\\\nexit 0\\\\n\" > {{ .Env.RELEASE_STANDARD_SCRIPTS }}/signing-gate.sh'")
if variant earlier-hook "    - '{{ .Env.RELEASE_STANDARD_SCRIPTS }}/signing-gate.sh'" \
	"$(printf '%s\n%s' "$hook_line" "    - '{{ .Env.RELEASE_STANDARD_SCRIPTS }}/signing-gate.sh'")"; then
	route earlier-hook "$SCRATCH/configs/earlier-hook.yml" signing-gate-hook-order
fi

# hook_route LABEL CONFIG: a hook that runs after the gate (the engine
# admits the run with require_signing=false, as a legitimately unsigned
# release would be) replaces the checksum-signing wrapper: the engine exits 0,
# the wrapper differs afterwards, the after-GoReleaser tree check refuses with
# 31, and the lint refuses the config as caller-hook.
hook_route() {
	local label=$1 config=$2 rc=0
	engine "$config" false || rc=$?
	if [ "$rc" -eq 0 ] && ! cmp -s "$SCRIPTS/cosign-sign-checksum.sh" "$REPO_ROOT/scripts/cosign-sign-checksum.sh"; then
		pass "$label-engine-route-live"
	else
		fail "$label-engine-route-live" "the hook no longer replaced the wrapper (rc=$rc); re-check this control against the engine: $(tail -20 "$SCRATCH/engine.log")"
	fi
	rc=0
	post_check || rc=$?
	if [ "$rc" -eq 31 ] && grep -Fq 'REFUSED[standard_tree_modified]' "$SCRATCH/post.log"; then
		pass "$label-post-check"
	else
		fail "$label-post-check" "rc=$rc: $(cat "$SCRATCH/post.log")"
	fi
	expect_lint "$label-lint" caller-hook "$config"
}

# Proves the r2 later-before route: a before hook after the gate copies the
# caller's wrapper over the shared one.
copy_line="    - 'cp ./caller-cosign.sh {{ .Env.RELEASE_STANDARD_SCRIPTS }}/cosign-sign-checksum.sh'"
if variant later-before "    - '{{ .Env.RELEASE_STANDARD_SCRIPTS }}/signing-gate.sh'" \
	"$(printf '%s\n%s' "    - '{{ .Env.RELEASE_STANDARD_SCRIPTS }}/signing-gate.sh'" "$copy_line")"; then
	hook_route later-before "$SCRATCH/configs/later-before.yml"
fi

# Proves the r2 build-pre route: the same copy in builds[0].hooks.pre.
if variant build-pre '    goos: [darwin, linux]' \
	"$(printf '%s\n%s\n%s' '    goos: [darwin, linux]' '    hooks:' "      pre: 'cp ./caller-cosign.sh {{ .Env.RELEASE_STANDARD_SCRIPTS }}/cosign-sign-checksum.sh'")"; then
	hook_route build-pre "$SCRATCH/configs/build-pre.yml"
fi

# Proves the r2 padded-id route: notarize ids [' relux-fixture'] select no
# Darwin binary in the engine's artifacts (the valid control above selects
# some), and the lint refuses the config as notarize-uncovered.
if variant padded-id '      ids: [relux-fixture]' "      ids: [' relux-fixture']"; then
	rc=0
	engine "$SCRATCH/configs/padded-id.yml" false || rc=$?
	selected=$(darwin_selected ' relux-fixture' || echo error)
	if [ "$rc" -eq 0 ] && [ "$selected" = 0 ]; then
		pass padded-id-engine-selects-nothing
	else
		fail padded-id-engine-selects-nothing "rc=$rc selected=$selected"
	fi
	expect_lint padded-id-lint notarize-uncovered "$SCRATCH/configs/padded-id.yml"
fi

# Proves the r2 whitespace env route: 'CGO_ENABLED=0 ' builds a host binary
# that go version -m does not record as CGO_ENABLED=0 (the valid control
# records 0), and the lint refuses it as build-cgo-enabled. Limit: needs a C
# toolchain on the host, as on the hosted Ubuntu runner.
if variant cgo-space 'env: [CGO_ENABLED=0, GOFLAGS=-mod=readonly]' "env: ['CGO_ENABLED=0 ', GOFLAGS=-mod=readonly]"; then
	rc=0
	# Host target only: a cgo-enabled cross build would need a cross C toolchain.
	engine "$SCRATCH/configs/cgo-space.yml" false --single-target || rc=$?
	cgo=$(build_setting CGO_ENABLED || true)
	if [ "$rc" -eq 0 ] && [ "$cgo" != 0 ]; then
		pass cgo-space-engine-route-live
	else
		fail cgo-space-engine-route-live "rc=$rc CGO_ENABLED='$cgo': $(tail -20 "$SCRATCH/engine.log")"
	fi
	expect_lint cgo-space-lint build-cgo-enabled "$SCRATCH/configs/cgo-space.yml"
fi

# Proves the tracked scripts were never handed to the engine.
if git -C "$REPO_ROOT" diff --quiet -- scripts; then
	pass tracked-scripts-untouched
else
	fail tracked-scripts-untouched 'scripts/ changed during the engine test'
fi

finish engine-test
