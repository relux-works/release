package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readTemplate(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "template", name))
	if err != nil {
		t.Fatalf("read template %s: %v", name, err)
	}
	return string(data)
}

// plant replaces exactly one occurrence of old, failing the test when the
// plant would not apply, so a stale case cannot pass by testing the template.
func plant(t *testing.T, text, old, replacement string) string {
	t.Helper()
	if n := strings.Count(text, old); n != 1 {
		t.Fatalf("plant %q matches %d times, want exactly 1", old, n)
	}
	return strings.Replace(text, old, replacement, 1)
}

func ids(found []Finding) []string {
	out := make([]string, 0, len(found))
	for _, f := range found {
		out = append(out, f.ID)
	}
	return out
}

func hasID(found []Finding, id string) bool {
	return contains(ids(found), id)
}

// Proves the positive control: the shipped template satisfies every rule
// (placeholders allowed), so the drift cases below cannot pass by rejecting all.
func TestTemplatePasses(t *testing.T) {
	found, err := LintGoReleaser([]byte(readTemplate(t, ".goreleaser.yml")), Options{AllowPlaceholders: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 0 {
		t.Fatalf("template has findings: %v", found)
	}
}

// Proves an unfilled copy of the template is refused: placeholders left in a
// caller config are a finding unless explicitly allowed.
func TestPlaceholdersRefusedByDefault(t *testing.T) {
	found, err := LintGoReleaser([]byte(readTemplate(t, ".goreleaser.yml")), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !hasID(found, "placeholder-left") {
		t.Fatalf("want placeholder-left, got %v", ids(found))
	}
}

// Proves each rule detects its own planted drift in an otherwise valid
// template. Each case names the single rule it targets; a case passes only if
// that rule fires, and the template positive above shows it does not fire
// on the unmodified file.
func TestPlantedDrift(t *testing.T) {
	cases := []struct {
		name, old, replacement, want string
	}{
		{"config version", "version: 2\n", "version: 1\n", "config-version"},
		{"cgo enabled", "CGO_ENABLED=0", "CGO_ENABLED=1", "build-cgo-enabled"},
		{"mod not readonly", "GOFLAGS=-mod=readonly", "GOFLAGS=-mod=mod", "build-mod-readonly"},
		{"no trimpath", "flags: [-trimpath, -buildvcs=false]", "flags: [-buildvcs=false]", "build-trimpath"},
		{"buildvcs on", "flags: [-trimpath, -buildvcs=false]", "flags: [-trimpath]", "build-buildvcs"},
		{"buildid kept", "-s -w -buildid= -X", "-s -w -X", "build-ldflags"},
		{"symbols kept", "-s -w -buildid= -X", "-buildid= -X", "build-ldflags"},
		{"plain go tool", "tool: '{{ .Env.RELEASE_STANDARD_SCRIPTS }}/go-toolchain.sh'", "tool: go", "build-toolchain-hook"},
		{"caller-local toolchain", "tool: '{{ .Env.RELEASE_STANDARD_SCRIPTS }}/go-toolchain.sh'", "tool: ./scripts/go-toolchain.sh", "build-toolchain-hook"},
		{"signing gate removed", "    - '{{ .Env.RELEASE_STANDARD_SCRIPTS }}/signing-gate.sh'", "    - 'true'", "signing-gate-hook"},
		{"notarize disabled", `enabled: '{{ isEnvSet "MACOS_SIGN_P12" }}'`, "enabled: 'false'", "notarize-missing"},
		{"notarize no wait", "wait: true", "wait: false", "notarize-missing"},
		{"no archlinux", "formats: [deb, rpm, archlinux]", "formats: [deb, rpm]", "nfpm-formats"},
		{"bindir moved", "bindir: /usr/bin", "bindir: /usr/local/bin", "nfpm-bindir"},
		{"checksum renamed", "name_template: checksums.txt", "name_template: sums.txt", "checksum-name"},
		{"cosign not on checksum", "artifacts: checksum", "artifacts: none", "cosign-checksum-sign"},
		{"sbom not on archive", "artifacts: archive", "artifacts: binary", "sbom-archive"},
		{"cask uploads rc", "skip_upload: auto", "skip_upload: false", "cask-skip-upload"},
		{"stable-only release", "prerelease: auto", "prerelease: false", "release-prerelease"},
	}
	template := readTemplate(t, ".goreleaser.yml")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			drifted := plant(t, template, tc.old, tc.replacement)
			found, err := LintGoReleaser([]byte(drifted), Options{AllowPlaceholders: true})
			if err != nil {
				t.Fatal(err)
			}
			if !hasID(found, tc.want) {
				t.Fatalf("want %s, got %v", tc.want, ids(found))
			}
		})
	}
}

// lintTemplate lints text with placeholders allowed and fails on parse errors.
func lintTemplate(t *testing.T, text string) []Finding {
	t.Helper()
	found, err := LintGoReleaser([]byte(text), Options{AllowPlaceholders: true})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

const gateHookLine = "    - '{{ .Env.RELEASE_STANDARD_SCRIPTS }}/signing-gate.sh'\n"

// Proves a caller config cannot set any protected name (signing policy,
// scripts path, the five signing inputs, the tap token, PATH) in root env,
// build env, before-hook env or build-hook env, nor hide one behind a
// template or a non-string entry. The positives next to it show unrelated
// names in the same places are accepted.
func TestProtectedEnv(t *testing.T) {
	template := readTemplate(t, ".goreleaser.yml")
	places := map[string]func(entry string) string{
		"root env": func(entry string) string {
			return plant(t, template, "version: 2\n", "version: 2\nenv:\n  - '"+entry+"'\n")
		},
		"build env": func(entry string) string {
			return plant(t, template, "env: [CGO_ENABLED=0, GOFLAGS=-mod=readonly]", "env: [CGO_ENABLED=0, GOFLAGS=-mod=readonly, '"+entry+"']")
		},
		"before hook env": func(entry string) string {
			return plant(t, template, gateHookLine, gateHookLine+"    - cmd: 'true'\n      env: ['"+entry+"']\n")
		},
		"build hook env": func(entry string) string {
			return plant(t, template, "    goos: [darwin, linux]\n", "    goos: [darwin, linux]\n    hooks:\n      pre:\n        - cmd: 'true'\n          env: ['"+entry+"']\n")
		},
	}
	for place, build := range places {
		for _, name := range protectedEnv {
			t.Run(place+"/"+name, func(t *testing.T) {
				if found := lintTemplate(t, build(name+"=dummy")); !hasID(found, "protected-env") {
					t.Fatalf("want protected-env, got %v", ids(found))
				}
			})
		}
		t.Run(place+"/templated name", func(t *testing.T) {
			if found := lintTemplate(t, build(`{{ "RELEASE_REQUIRE_SIGNING" }}=false`)); !hasID(found, "protected-env") {
				t.Fatalf("want protected-env, got %v", ids(found))
			}
		})
		t.Run(place+"/unrelated name control", func(t *testing.T) {
			if found := lintTemplate(t, build("RELUX_FIXTURE_UNRELATED=1")); len(found) != 0 {
				t.Fatalf("unrelated env name: unexpected findings %v", found)
			}
		})
	}
	nonString := plant(t, template, "version: 2\n", "version: 2\nenv:\n  - {RELEASE_REQUIRE_SIGNING: false}\n")
	if found := lintTemplate(t, nonString); !hasID(found, "protected-env") {
		t.Fatalf("non-string env entry: want protected-env, got %v", ids(found))
	}
}

// Proves the signing gate must be the first before hook in plain form: an
// earlier hook (here the review's route, rewriting the shared gate before it
// runs) and a gate hook with its own env or dir are refused, and a gate
// replaced by another script is reported as missing. The positives show a
// hook after the gate and the gate in cmd-only map form are accepted.
func TestSigningGateHookOrder(t *testing.T) {
	template := readTemplate(t, ".goreleaser.yml")
	rewrite := "    - sh -c 'printf \"#!/bin/sh\\nexit 0\\n\" > {{ .Env.RELEASE_STANDARD_SCRIPTS }}/signing-gate.sh'\n"
	cases := []struct {
		name, text, want string
	}{
		{"earlier rewrite hook", plant(t, template, gateHookLine, rewrite+gateHookLine), "signing-gate-hook-order"},
		{"earlier harmless hook", plant(t, template, gateHookLine, "    - 'true'\n"+gateHookLine), "signing-gate-hook-order"},
		{"replacement gate", plant(t, template, gateHookLine, "    - '{{ .Env.RELEASE_STANDARD_SCRIPTS }}/../fake/signing-gate.sh'\n"), "signing-gate-hook"},
		{"gate with env", plant(t, template, gateHookLine, "    - cmd: '{{ .Env.RELEASE_STANDARD_SCRIPTS }}/signing-gate.sh'\n      env: [RELUX_FIXTURE_UNRELATED=1]\n"), "signing-gate-hook-order"},
		{"gate with dir", plant(t, template, gateHookLine, "    - cmd: '{{ .Env.RELEASE_STANDARD_SCRIPTS }}/signing-gate.sh'\n      dir: /tmp\n"), "signing-gate-hook-order"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if found := lintTemplate(t, tc.text); !hasID(found, tc.want) {
				t.Fatalf("want %s, got %v", tc.want, ids(found))
			}
		})
	}
	for name, text := range map[string]string{
		"later hook":    plant(t, template, gateHookLine, gateHookLine+"    - 'true'\n"),
		"cmd-only gate": plant(t, template, gateHookLine, "    - cmd: '{{ .Env.RELEASE_STANDARD_SCRIPTS }}/signing-gate.sh'\n"),
	} {
		t.Run(name+" control", func(t *testing.T) {
			if found := lintTemplate(t, text); len(found) != 0 {
				t.Fatalf("unexpected findings %v", found)
			}
		})
	}
}

const secondDarwinBuild = `  - id: second
    main: __MAIN_PACKAGE__
    binary: second
    tool: '{{ .Env.RELEASE_STANDARD_SCRIPTS }}/go-toolchain.sh'
    env: [CGO_ENABLED=0, GOFLAGS=-mod=readonly]
    __GOOS__
    goarch: [arm64]
    flags: [-trimpath, -buildvcs=false]
    ldflags:
      - -s -w -buildid=

archives:
`

// withSecondBuild adds a second build whose target line is goos (for
// example "goos: [darwin]") and, when ids is not empty, sets notarize ids.
func withSecondBuild(t *testing.T, template, goos, ids string) string {
	t.Helper()
	text := plant(t, template, "\narchives:\n", "\n"+strings.Replace(secondDarwinBuild, "__GOOS__", goos, 1))
	if ids != "" {
		text = plant(t, text, "      ids: [__BINARY__]\n      sign:", "      ids: "+ids+"\n      sign:")
	}
	return text
}

// Proves notarization must select every releasable Darwin build: a wrong
// id, a subset of the Darwin builds, an extra Darwin build (by goos, by
// default goos and by targets) and an id that names no build are each
// refused as notarize-uncovered. The positives show full coverage, default
// ids resolving to the project name, a Linux-only extra build and a skipped
// Darwin build are accepted.
func TestNotarizeCoverage(t *testing.T) {
	template := readTemplate(t, ".goreleaser.yml")
	negative := map[string]string{
		"wrong id":             plant(t, template, "      ids: [__BINARY__]\n      sign:", "      ids: [nonexistent]\n      sign:"),
		"subset of ids":        withSecondBuild(t, template, "goos: [darwin]", "[__BINARY__]"),
		"extra darwin build":   withSecondBuild(t, template, "goos: [darwin]", ""),
		"extra default goos":   withSecondBuild(t, template, "# goos defaults", ""),
		"extra darwin targets": withSecondBuild(t, template, "targets: [darwin_arm64]", ""),
		"unknown extra id":     plant(t, template, "      ids: [__BINARY__]\n      sign:", "      ids: [__BINARY__, ghost]\n      sign:"),
	}
	for name, text := range negative {
		t.Run(name, func(t *testing.T) {
			if found := lintTemplate(t, text); !hasID(found, "notarize-uncovered") {
				t.Fatalf("want notarize-uncovered, got %v", ids(found))
			}
		})
	}
	positive := map[string]string{
		"full coverage":      withSecondBuild(t, template, "goos: [darwin]", "[__BINARY__, second]"),
		"default ids":        plant(t, template, "      ids: [__BINARY__]\n      sign:", "      sign:"),
		"linux-only extra":   withSecondBuild(t, template, "goos: [linux]", ""),
		"skipped darwin":     withSecondBuild(t, template, "goos: [darwin]\n    skip: true", ""),
		"linux-only targets": withSecondBuild(t, template, "targets: [linux_arm64]", ""),
	}
	for name, text := range positive {
		t.Run(name+" control", func(t *testing.T) {
			if found := lintTemplate(t, text); len(found) != 0 {
				t.Fatalf("unexpected findings %v", found)
			}
		})
	}
}

// Proves the lint checks the effective build settings, not token presence:
// each case keeps the required tokens and adds a later assignment or option
// that would override them in the real build (the review's CGO_ENABLED=1,
// VCS stamping and non-empty build id among them), and must fire the named
// rule. The positive shows unrelated env, flags and -X values next to the
// standard ones are accepted, so the rules do not reject any addition.
func TestEffectiveBuildSettings(t *testing.T) {
	const (
		envLine   = "env: [CGO_ENABLED=0, GOFLAGS=-mod=readonly]"
		flagsLine = "flags: [-trimpath, -buildvcs=false]"
		ldLine    = "      - -s -w -buildid= -X __VERSION_SYMBOL__={{ .Version }}"
	)
	template := readTemplate(t, ".goreleaser.yml")
	cases := []struct{ name, old, replacement, want string }{
		{"cgo override", envLine, "env: [CGO_ENABLED=0, GOFLAGS=-mod=readonly, CGO_ENABLED=1]", "build-cgo-enabled"},
		{"cgo duplicate", envLine, "env: [CGO_ENABLED=0, GOFLAGS=-mod=readonly, CGO_ENABLED=0]", "build-cgo-enabled"},
		{"goflags override", envLine, "env: [CGO_ENABLED=0, GOFLAGS=-mod=readonly, GOFLAGS=-mod=mod]", "build-mod-readonly"},
		{"goflags extended", envLine, "env: [CGO_ENABLED=0, 'GOFLAGS=-mod=readonly -mod=mod']", "build-mod-readonly"},
		{"trimpath disabled", flagsLine, "flags: [-trimpath, -buildvcs=false, -trimpath=false]", "build-trimpath"},
		{"buildvcs re-enabled", flagsLine, "flags: [-trimpath, -buildvcs=false, -buildvcs=true]", "build-buildvcs"},
		{"buildvcs bare", flagsLine, "flags: [-trimpath, -buildvcs=false, -buildvcs]", "build-buildvcs"},
		{"mod flag", flagsLine, "flags: [-trimpath, -buildvcs=false, -mod=mod]", "build-mod-readonly"},
		{"mod two-arg flag", flagsLine, "flags: [-trimpath, -buildvcs=false, -mod, mod]", "build-mod-readonly"},
		{"ldflags in flags", flagsLine, "flags: [-trimpath, -buildvcs=false, '-ldflags=-buildid=x']", "build-ldflags"},
		{"templated flag", flagsLine, "flags: [-trimpath, -buildvcs=false, '{{ if true }}-buildvcs=true{{ end }}']", "build-flags-template"},
		{"buildid override", ldLine, ldLine + " -buildid=review-not-empty", "build-ldflags"},
		{"symbols re-enabled", ldLine, ldLine + " -s=false", "build-ldflags"},
		{"dwarf re-enabled", ldLine, ldLine + " -w=0", "build-ldflags"},
		{"x swallows -s", ldLine, "      - -w -buildid= -X -s", "build-ldflags"},
		{"env template in ldflags", ldLine, ldLine + " {{ .Env.LDX }}", "build-ldflags"},
		{"quoted ldflags", ldLine, ldLine + ` '-buildid=x'`, "build-ldflags"},
		{"other linker flag", ldLine, ldLine + " -linkmode=external", "build-ldflags"},
		{"target overrides", "    goarch: [arm64, amd64]\n", "    goarch: [arm64, amd64]\n    overrides:\n      - goos: darwin\n        env: [CGO_ENABLED=1]\n", "build-overrides"},
		{"gobinary", "    goarch: [arm64, amd64]\n", "    goarch: [arm64, amd64]\n    gobinary: go\n", "build-toolchain-hook"},
		{"other builder", "    goarch: [arm64, amd64]\n", "    goarch: [arm64, amd64]\n    builder: rust\n", "build-toolchain-hook"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if found := lintTemplate(t, plant(t, template, tc.old, tc.replacement)); !hasID(found, tc.want) {
				t.Fatalf("want %s, got %v", tc.want, ids(found))
			}
		})
	}
	benign := plant(t, template, envLine, "env: [CGO_ENABLED=0, GOFLAGS=-mod=readonly, GOPRIVATE=example.invalid]")
	benign = plant(t, benign, flagsLine, "flags: [-trimpath, -buildvcs=false, -tags=netgo, -mod=readonly]")
	benign = plant(t, benign, ldLine, ldLine+" -X main.commit={{ .Commit }} -X=main.date={{ .Date }}")
	if found := lintTemplate(t, benign); len(found) != 0 {
		t.Fatalf("benign additions: unexpected findings %v", found)
	}
}

// Proves the workflow inputs are cross-checked: a main/binary that the config
// does not build is a finding, and the matching values are not.
func TestExpectMainAndBinary(t *testing.T) {
	data := []byte(readTemplate(t, ".goreleaser.yml"))
	ok, err := LintGoReleaser(data, Options{AllowPlaceholders: true, ExpectMain: "__MAIN_PACKAGE__", ExpectBinary: "__BINARY__"})
	if err != nil {
		t.Fatal(err)
	}
	if len(ok) != 0 {
		t.Fatalf("matching inputs: unexpected findings %v", ok)
	}
	bad, err := LintGoReleaser(data, Options{AllowPlaceholders: true, ExpectMain: "./cmd/other", ExpectBinary: "other"})
	if err != nil {
		t.Fatal(err)
	}
	if !hasID(bad, "build-main-mismatch") || !hasID(bad, "build-binary-mismatch") {
		t.Fatalf("want both mismatch findings, got %v", ids(bad))
	}
}

// Proves malformed input is an error (exit 2), not a clean pass.
func TestParseErrors(t *testing.T) {
	for _, in := range []string{"", "version: [", "- just\n- a list\n"} {
		if _, err := LintGoReleaser([]byte(in), Options{}); err == nil {
			t.Errorf("input %q: want parse error", in)
		}
	}
}

// Proves the caller workflow template passes, and that an unpinned standard
// ref, a different reusable workflow and a floating GoReleaser version each
// fail with their own finding while an exact version passes.
func TestWorkflow(t *testing.T) {
	template := readTemplate(t, "release.yml")
	found, err := LintWorkflow([]byte(template), Options{AllowPlaceholders: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 0 {
		t.Fatalf("workflow template has findings: %v", found)
	}

	pinned := plant(t, template, "@__STANDARD_SHA__", "@0123456789abcdef0123456789abcdef01234567")
	if found, _ := LintWorkflow([]byte(pinned), Options{AllowPlaceholders: true}); len(found) != 0 {
		t.Fatalf("full SHA pin: unexpected findings %v", found)
	}
	unpinned := plant(t, template, "@__STANDARD_SHA__", "@main")
	if found, _ := LintWorkflow([]byte(unpinned), Options{AllowPlaceholders: true}); !hasID(found, "standard-workflow-unpinned") {
		t.Fatalf("@main: want standard-workflow-unpinned, got %v", ids(found))
	}
	other := plant(t, template, standardRepository+"/.github/workflows/go-release.yml", "someone/else/.github/workflows/go-release.yml")
	if found, _ := LintWorkflow([]byte(other), Options{AllowPlaceholders: true}); !hasID(found, "standard-workflow-missing") {
		t.Fatalf("other repo: want standard-workflow-missing, got %v", ids(found))
	}

	direct := func(version string) []byte {
		return []byte("on: push\njobs:\n  release:\n    runs-on: macos-15\n    steps:\n" +
			"      - uses: goreleaser/goreleaser-action@e435ccd777264be153ace6237001ef4d979d3a7a\n" +
			"        with:\n          version: " + version + "\n")
	}
	if found, _ := LintWorkflow(direct("'~> v2'"), Options{}); !hasID(found, "goreleaser-unpinned") {
		t.Fatalf("~> v2: want goreleaser-unpinned, got %v", ids(found))
	}
	if found, _ := LintWorkflow(direct("v2.17.0"), Options{}); hasID(found, "goreleaser-unpinned") {
		t.Fatalf("v2.17.0: unexpected goreleaser-unpinned")
	}
}

// Proves the CLI exit-code contract: 0 for the template, 1 with the finding
// ID on stdout for a drift, 2 for usage and unreadable files.
func TestRunExitCodes(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.yml")
	bad := filepath.Join(dir, "bad.yml")
	template := readTemplate(t, ".goreleaser.yml")
	if err := os.WriteFile(good, []byte(template), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bad, []byte(plant(t, template, "prerelease: auto", "prerelease: false")), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if rc := run([]string{"-allow-placeholders", good}, &out, &errOut); rc != 0 {
		t.Fatalf("template: rc=%d stdout=%s stderr=%s", rc, out.String(), errOut.String())
	}
	out.Reset()
	if rc := run([]string{"-allow-placeholders", bad}, &out, &errOut); rc != 1 || !strings.Contains(out.String(), ": release-prerelease: ") {
		t.Fatalf("drift: rc=%d stdout=%s", rc, out.String())
	}
	if rc := run([]string{filepath.Join(dir, "absent.yml")}, &out, &errOut); rc != 2 {
		t.Fatalf("absent file: rc=%d, want 2", rc)
	}
	if rc := run(nil, &out, &errOut); rc != 2 {
		t.Fatalf("no args: rc=%d, want 2", rc)
	}
}
