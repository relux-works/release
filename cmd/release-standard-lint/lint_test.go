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
