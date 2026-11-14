#!/usr/bin/env bash
# Real-engine controls for the signing admission: runs the pinned GoReleaser
# (v2.17.0) on a throwaway caller fixture built from the template, with no
# signing field and no publication (`build --snapshot`). GORELEASER names the
# binary (default: goreleaser on PATH). Without it the test prints SKIP and
# exits 0, unless REQUIRE_ENGINE=1 (hosted CI), where a missing or other
# engine version fails. The tracked scripts are never run by the engine: it
# gets a scratch copy, which the hook route below overwrites.
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
SCRIPTS="$SCRATCH/standard-scripts"

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

# engine CONFIG REQUIRE: fresh scripts copy, then a snapshot build with no
# signing field. Output in $SCRATCH/engine.log; returns GoReleaser's rc.
engine() {
	rm -rf "$SCRIPTS"
	cp -R "$REPO_ROOT/scripts" "$SCRIPTS"
	env -u MACOS_SIGN_P12 -u MACOS_SIGN_P12_PASSWORD -u ASC_ISSUER_ID -u ASC_KEY_ID -u ASC_KEY_P8 \
		-u TAP_GITHUB_TOKEN -u GITHUB_TOKEN \
		RELEASE_STANDARD_SCRIPTS="$SCRIPTS" RELEASE_REQUIRE_SIGNING="$2" \
		"$GORELEASER" build --snapshot --clean --single-target --config "$1" >"$SCRATCH/engine.log" 2>&1
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

# Proves the tracked scripts were never handed to the engine.
if git -C "$REPO_ROOT" diff --quiet -- scripts; then
	pass tracked-scripts-untouched
else
	fail tracked-scripts-untouched 'scripts/ changed during the engine test'
fi

finish engine-test
