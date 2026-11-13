package main

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// standardRepository is the shared standard repository (placeholder name;
// rename here only, see NOTES.md).
const standardRepository = "relux-works/release"

const (
	scriptsPrefix  = "{{ .Env.RELEASE_STANDARD_SCRIPTS }}/"
	toolchainHook  = scriptsPrefix + "go-toolchain.sh"
	signingGate    = scriptsPrefix + "signing-gate.sh"
	notarizeEnable = `{{ isEnvSet "MACOS_SIGN_P12" }}`
)

var (
	placeholderRE  = regexp.MustCompile(`__[A-Z][A-Z0-9_]*__`)
	fullSHARE      = regexp.MustCompile(`^[0-9a-f]{40}$`)
	exactVersionRE = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)
)

// Finding is one named violation of the release standard.
type Finding struct {
	ID      string
	Message string
}

// Options tune a lint run.
type Options struct {
	// AllowPlaceholders accepts __PLACEHOLDER__ tokens (template checks only).
	AllowPlaceholders bool
	// ExpectMain and ExpectBinary, when set, must match one builds[] entry.
	ExpectMain   string
	ExpectBinary string
}

type findings []Finding

func (f *findings) add(id, format string, args ...any) {
	*f = append(*f, Finding{ID: id, Message: fmt.Sprintf(format, args...)})
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}

func asString(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

// stringList accepts a scalar string or a list of strings.
func stringList(v any) []string {
	switch t := v.(type) {
	case string:
		return []string{strings.TrimSpace(t)}
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s, ok := e.(string); ok {
				out = append(out, strings.TrimSpace(s))
			}
		}
		return out
	}
	return nil
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func parse(data []byte) (map[string]any, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	if doc == nil {
		return nil, fmt.Errorf("parse: empty document")
	}
	return doc, nil
}

func checkPlaceholders(data []byte, opt Options, f *findings) {
	if opt.AllowPlaceholders {
		return
	}
	if m := placeholderRE.FindAllString(string(data), -1); len(m) > 0 {
		seen := map[string]bool{}
		var uniq []string
		for _, p := range m {
			if !seen[p] {
				seen[p] = true
				uniq = append(uniq, p)
			}
		}
		f.add("placeholder-left", "template placeholders left: %s", strings.Join(uniq, ", "))
	}
}

// LintGoReleaser checks a caller .goreleaser.yml against template/.goreleaser.yml.
func LintGoReleaser(data []byte, opt Options) ([]Finding, error) {
	doc, err := parse(data)
	if err != nil {
		return nil, err
	}
	var f findings
	checkPlaceholders(data, opt, &f)

	if v, ok := doc["version"].(int); !ok || v != 2 {
		f.add("config-version", "version must be 2")
	}

	hookFound := false
	for _, h := range asList(asMap(doc["before"])["hooks"]) {
		cmd := asString(h)
		if cmd == "" {
			cmd = asString(asMap(h)["cmd"])
		}
		if cmd == signingGate {
			hookFound = true
		}
	}
	if !hookFound {
		f.add("signing-gate-hook", "before.hooks must run %s", signingGate)
	}

	builds := asList(doc["builds"])
	if len(builds) == 0 {
		f.add("builds-missing", "builds must declare at least one Go build")
	}
	mainFound, binaryFound := false, false
	for i, b := range builds {
		m := asMap(b)
		where := fmt.Sprintf("builds[%d]", i)
		if opt.ExpectMain != "" && asString(m["main"]) == opt.ExpectMain {
			mainFound = true
		}
		if opt.ExpectBinary != "" && asString(m["binary"]) == opt.ExpectBinary {
			binaryFound = true
		}
		env := stringList(m["env"])
		if !contains(env, "CGO_ENABLED=0") {
			f.add("build-cgo-enabled", "%s: env must contain CGO_ENABLED=0", where)
		}
		if !contains(env, "GOFLAGS=-mod=readonly") {
			f.add("build-mod-readonly", "%s: env must contain GOFLAGS=-mod=readonly", where)
		}
		flags := stringList(m["flags"])
		if !contains(flags, "-trimpath") {
			f.add("build-trimpath", "%s: flags must contain -trimpath", where)
		}
		if !contains(flags, "-buildvcs=false") {
			f.add("build-buildvcs", "%s: flags must contain -buildvcs=false", where)
		}
		ld := strings.Fields(strings.Join(stringList(m["ldflags"]), " "))
		var missing []string
		for _, want := range []string{"-s", "-w", "-buildid="} {
			if !contains(ld, want) {
				missing = append(missing, want)
			}
		}
		if len(missing) > 0 {
			f.add("build-ldflags", "%s: ldflags missing %s", where, strings.Join(missing, " "))
		}
		if asString(m["tool"]) != toolchainHook {
			f.add("build-toolchain-hook", "%s: tool must be %s", where, toolchainHook)
		}
	}
	if opt.ExpectMain != "" && !mainFound {
		f.add("build-main-mismatch", "no build has main %q (workflow input main_package)", opt.ExpectMain)
	}
	if opt.ExpectBinary != "" && !binaryFound {
		f.add("build-binary-mismatch", "no build has binary %q (workflow input binary)", opt.ExpectBinary)
	}

	if !notarizeOK(asList(asMap(doc["notarize"])["macos"])) {
		f.add("notarize-missing", "notarize.macos needs an entry enabled by %s that signs with MACOS_SIGN_P12/MACOS_SIGN_P12_PASSWORD and notarizes with ASC_ISSUER_ID/ASC_KEY_ID/ASC_KEY_P8 and wait: true", notarizeEnable)
	}

	nfpms := asList(doc["nfpms"])
	if len(nfpms) == 0 {
		f.add("nfpm-missing", "nfpms must declare the Linux packages")
	}
	for i, n := range nfpms {
		m := asMap(n)
		formats := stringList(m["formats"])
		for _, want := range []string{"deb", "rpm", "archlinux"} {
			if !contains(formats, want) {
				f.add("nfpm-formats", "nfpms[%d]: formats must include %s", i, want)
			}
		}
		if asString(m["bindir"]) != "/usr/bin" {
			f.add("nfpm-bindir", "nfpms[%d]: bindir must be /usr/bin", i)
		}
	}

	checksum := asMap(doc["checksum"])
	if d, _ := checksum["disable"].(bool); d || asString(checksum["name_template"]) != "checksums.txt" {
		f.add("checksum-name", "checksum must be enabled with name_template checksums.txt")
	}

	cosignFound := false
	for _, s := range asList(doc["signs"]) {
		m := asMap(s)
		if asString(m["cmd"]) == "cosign" && asString(m["artifacts"]) == "checksum" &&
			contains(stringList(m["args"]), "sign-blob") {
			cosignFound = true
		}
	}
	if !cosignFound {
		f.add("cosign-checksum-sign", "signs must run cosign sign-blob over artifacts: checksum")
	}

	sbomFound := false
	for _, s := range asList(doc["sboms"]) {
		if asString(asMap(s)["artifacts"]) == "archive" {
			sbomFound = true
		}
	}
	if !sbomFound {
		f.add("sbom-archive", "sboms must include an entry with artifacts: archive")
	}

	for i, c := range asList(doc["homebrew_casks"]) {
		if v, ok := asMap(c)["skip_upload"].(string); !ok || v != "auto" {
			f.add("cask-skip-upload", "homebrew_casks[%d]: skip_upload must be auto", i)
		}
	}
	for _, key := range []string{"brews", "scoops"} {
		for i, c := range asList(doc[key]) {
			v := asMap(c)["skip_upload"]
			if s, ok := v.(string); ok && s == "auto" {
				continue
			}
			if b, ok := v.(bool); ok && b {
				continue
			}
			f.add("skip-upload", "%s[%d]: skip_upload must be auto or true", key, i)
		}
	}

	if asString(asMap(doc["release"])["prerelease"]) != "auto" {
		f.add("release-prerelease", "release.prerelease must be auto")
	}
	return f, nil
}

func notarizeOK(entries []any) bool {
	for _, e := range entries {
		m := asMap(e)
		sign := asMap(m["sign"])
		notarize := asMap(m["notarize"])
		wait, _ := notarize["wait"].(bool)
		if asString(m["enabled"]) == notarizeEnable &&
			asString(sign["certificate"]) == "{{ .Env.MACOS_SIGN_P12 }}" &&
			asString(sign["password"]) == "{{ .Env.MACOS_SIGN_P12_PASSWORD }}" &&
			asString(notarize["issuer_id"]) == "{{ .Env.ASC_ISSUER_ID }}" &&
			asString(notarize["key_id"]) == "{{ .Env.ASC_KEY_ID }}" &&
			asString(notarize["key"]) == "{{ .Env.ASC_KEY_P8 }}" &&
			wait {
			return true
		}
	}
	return false
}

// LintWorkflow checks a caller release workflow: it must call the shared
// go-release.yml pinned to a full commit SHA, and any direct GoReleaser
// action must pin an exact GoReleaser version.
func LintWorkflow(data []byte, opt Options) ([]Finding, error) {
	doc, err := parse(data)
	if err != nil {
		return nil, err
	}
	var f findings
	checkPlaceholders(data, opt, &f)

	prefix := standardRepository + "/.github/workflows/go-release.yml@"
	jobs := asMap(doc["jobs"])
	names := make([]string, 0, len(jobs))
	for name := range jobs {
		names = append(names, name)
	}
	sort.Strings(names)
	found := false
	for _, name := range names {
		job := asMap(jobs[name])
		if uses := asString(job["uses"]); strings.HasPrefix(uses, prefix) {
			found = true
			ref := strings.TrimPrefix(uses, prefix)
			pinned := fullSHARE.MatchString(ref) ||
				(opt.AllowPlaceholders && placeholderRE.MatchString(ref))
			if !pinned {
				f.add("standard-workflow-unpinned", "jobs.%s: %s must be pinned to a full commit SHA, got %q", name, prefix, ref)
			}
		}
		for i, s := range asList(job["steps"]) {
			step := asMap(s)
			if strings.HasPrefix(asString(step["uses"]), "goreleaser/goreleaser-action@") {
				v := asString(asMap(step["with"])["version"])
				if !exactVersionRE.MatchString(v) {
					f.add("goreleaser-unpinned", "jobs.%s.steps[%d]: GoReleaser version must be an exact vX.Y.Z, got %q", name, i, v)
				}
			}
		}
	}
	if !found {
		f.add("standard-workflow-missing", "no job uses %s<sha>", prefix)
	}
	return f, nil
}
