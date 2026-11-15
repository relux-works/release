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
	cosignWrapper  = scriptsPrefix + "cosign-sign-checksum.sh"
	notarizeEnable = `{{ isEnvSet "MACOS_SIGN_P12" }}`
)

// protectedEnv are the names a caller config must never set in root env,
// build env or hook env. They steer the signing policy, the path of the
// shared scripts, the five signing inputs and the tap token, all of which the
// reusable workflow sets from its own inputs and secrets. GoReleaser applies
// a config's root env over the process env before it expands any hook, so
// one such entry would redirect or disable the in-GoReleaser gate. PATH is
// protected too: it decides which sh and cosign the shared scripts run. The
// other forge tokens are protected so a config cannot swap the credential
// GoReleaser picks for the release.
var protectedEnv = []string{
	"RELEASE_REQUIRE_SIGNING",
	"RELEASE_STANDARD_SCRIPTS",
	"MACOS_SIGN_P12",
	"MACOS_SIGN_P12_PASSWORD",
	"ASC_ISSUER_ID",
	"ASC_KEY_ID",
	"ASC_KEY_P8",
	"TAP_GITHUB_TOKEN",
	"PATH",
	"GITLAB_TOKEN",
	"GITEA_TOKEN",
}

// protectedEnvPrefixes extend protectedEnv to whole families: GITHUB_* (the
// release token and the run's identity), GORELEASER_* (token choice and the
// current tag), ACTIONS_* (the runner's OIDC request token and URL that
// keyless cosign uses), and COSIGN_* / SIGSTORE_* (where cosign sends that
// identity and what it trusts).
var protectedEnvPrefixes = []string{"GITHUB_", "GORELEASER_", "ACTIONS_", "COSIGN_", "SIGSTORE_"}

func isProtectedEnv(name string) bool {
	if contains(protectedEnv, name) {
		return true
	}
	for _, p := range protectedEnvPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

var (
	envNameRE      = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
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
	// ExpectRepository, when set, is the OWNER/NAME release.github must name.
	ExpectRepository string
}

// FindingIDs lists every finding ID the lint can report. add refuses any
// other ID, and the test suite's TestMain requires a driving negative test
// for each one, so a new rule cannot ship without a test.
var FindingIDs = []string{
	"placeholder-left",
	"config-version",
	"protected-env",
	"signing-gate-hook",
	"signing-gate-hook-order",
	"builds-missing",
	"build-cgo-enabled",
	"build-mod-readonly",
	"build-trimpath",
	"build-buildvcs",
	"build-flags-template",
	"build-ldflags",
	"build-toolchain-hook",
	"build-overrides",
	"build-main-mismatch",
	"build-binary-mismatch",
	"notarize-missing",
	"notarize-uncovered",
	"nfpm-missing",
	"nfpm-formats",
	"nfpm-bindir",
	"checksum-name",
	"cosign-checksum-sign",
	"sbom-archive",
	"cask-skip-upload",
	"skip-upload",
	"release-prerelease",
	"standard-workflow-unpinned",
	"standard-workflow-missing",
	"goreleaser-unpinned",
	"caller-hook",
	"release-target",
	"tap-repository",
	"env-reference",
}

type findings []Finding

func (f *findings) add(id, format string, args ...any) {
	if !contains(FindingIDs, id) {
		panic("release-standard-lint: unregistered finding ID " + id)
	}
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

// rawString returns a string value exactly as GoReleaser receives it, with
// no whitespace trimming.
func rawString(v any) string {
	s, _ := v.(string)
	return s
}

// rawStringList is stringList without trimming: a scalar string or the
// string entries of a list, byte for byte.
func rawStringList(v any) []string {
	switch t := v.(type) {
	case string:
		return []string{t}
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
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

	checkEnv("env", doc["env"], false, &f)
	checkBeforeHooks(asList(asMap(doc["before"])["hooks"]), &f)

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
		checkEnv(where+".env", m["env"], true, &f)
		for _, phase := range []string{"pre", "post"} {
			for j, h := range asList(asMap(m["hooks"])[phase]) {
				checkEnv(fmt.Sprintf("%s.hooks.%s[%d].env", where, phase, j), asMap(h)["env"], true, &f)
			}
		}
		if hooks, ok := m["hooks"]; ok && hooks != nil {
			f.add("caller-hook", "%s.hooks must not be set: build hooks run caller code after the signing gate and could replace the shared scripts", where)
		}
		checkBuildEnv(where, m["env"], &f)
		checkBuildFlags(where, rawStringList(m["flags"]), &f)
		checkLdflags(where, stringList(m["ldflags"]), &f)
		if asString(m["tool"]) != toolchainHook {
			f.add("build-toolchain-hook", "%s: tool must be %s", where, toolchainHook)
		}
		if _, ok := m["gobinary"]; ok {
			f.add("build-toolchain-hook", "%s: gobinary must not be set; tool is %s", where, toolchainHook)
		}
		if builder := asString(m["builder"]); builder != "" && builder != "go" {
			f.add("build-toolchain-hook", "%s: builder must be go, got %q", where, builder)
		}
		if _, ok := m["overrides"]; ok {
			f.add("build-overrides", "%s: overrides must not be set; per-target env, flags or ldflags would replace the standard ones", where)
		}
	}
	if opt.ExpectMain != "" && !mainFound {
		f.add("build-main-mismatch", "no build has main %q (workflow input main_package)", opt.ExpectMain)
	}
	if opt.ExpectBinary != "" && !binaryFound {
		f.add("build-binary-mismatch", "no build has binary %q (workflow input binary)", opt.ExpectBinary)
	}

	checkNotarize(doc, builds, &f)

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

	checkSigns(asList(doc["signs"]), &f)
	checkCallerCode(doc, &f)

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
	checkReleaseTarget(doc, opt, &f)
	checkTapRepository(doc, &f)
	checkTemplateEnv(doc, &f)
	return f, nil
}

// envValues returns, per name, every value a KEY=VALUE list assigns in
// order. Entries that are not strings are skipped (checkEnv refuses them).
func envValues(v any) map[string][]string {
	out := map[string][]string{}
	for _, e := range asList(v) {
		s, ok := e.(string)
		if !ok {
			continue
		}
		// Raw bytes: GoReleaser hands the entry to the build unchanged, and
		// "0 " is not the value that disables cgo.
		name, value, _ := strings.Cut(s, "=")
		out[name] = append(out[name], value)
	}
	return out
}

// checkBuildEnv requires CGO_ENABLED=0 and GOFLAGS=-mod=readonly as the only
// assignment of each name. A later duplicate would win in the build
// environment, so any second assignment, even an equal one, is refused.
func checkBuildEnv(where string, v any, f *findings) {
	values := envValues(v)
	for _, rule := range []struct{ id, name, want string }{
		{"build-cgo-enabled", "CGO_ENABLED", "0"},
		{"build-mod-readonly", "GOFLAGS", "-mod=readonly"},
	} {
		got := values[rule.name]
		if len(got) != 1 || got[0] != rule.want {
			f.add(rule.id, "%s: env must assign %s=%s exactly once, got %q", where, rule.name, rule.want, got)
		}
	}
}

// flagName returns the name of a command-line flag token (one or two
// leading dashes, up to "="), or "" when the token is not a flag.
func flagName(token string) string {
	if !strings.HasPrefix(token, "-") {
		return ""
	}
	name := strings.TrimPrefix(strings.TrimPrefix(token, "-"), "-")
	name, _, _ = strings.Cut(name, "=")
	return name
}

// checkBuildFlags validates the effective go build flags. GoReleaser passes
// each entry as one argument and the go command keeps the last value of a
// repeated flag, so -trimpath and -buildvcs=false must each appear exactly
// once in that form; -mod may only repeat -mod=readonly (a flag beats
// GOFLAGS); -ldflags belongs in ldflags; templated entries cannot be checked.
func checkBuildFlags(where string, flags []string, f *findings) {
	counts := map[string][]string{}
	for _, flag := range flags {
		if strings.Contains(flag, "{{") {
			f.add("build-flags-template", "%s: flag %q is templated and cannot be checked", where, flag)
			continue
		}
		name := flagName(flag)
		counts[name] = append(counts[name], flag)
	}
	if got := counts["trimpath"]; len(got) != 1 || got[0] != "-trimpath" {
		f.add("build-trimpath", "%s: flags must contain -trimpath exactly once and no other trimpath setting, got %q", where, got)
	}
	if got := counts["buildvcs"]; len(got) != 1 || got[0] != "-buildvcs=false" {
		f.add("build-buildvcs", "%s: flags must contain -buildvcs=false exactly once and no other buildvcs setting, got %q", where, got)
	}
	for _, flag := range counts["mod"] {
		if flag != "-mod=readonly" {
			f.add("build-mod-readonly", "%s: flag %q overrides GOFLAGS=-mod=readonly", where, flag)
		}
	}
	if got := counts["ldflags"]; len(got) > 0 {
		f.add("build-ldflags", "%s: flags must not set -ldflags (%q); use ldflags", where, got)
	}
	if got := counts["toolexec"]; len(got) > 0 {
		f.add("caller-hook", "%s: flags must not set -toolexec (%q): it runs a caller program for every compile step", where, got)
	}
}

// ldflagsTemplateRE matches the simple template fields allowed in ldflags;
// they expand to one token without spaces.
var ldflagsTemplateRE = regexp.MustCompile(`\{\{\s*\.(Version|Tag|Commit|ShortCommit|FullCommit|Date|CommitDate|CommitTimestamp|Timestamp|Major|Minor|Patch)\s*\}\}`)

// checkLdflags validates the effective linker flags. GoReleaser joins the
// entries with spaces into one -ldflags value, which go splits on spaces and
// the linker parses with last-wins semantics. Only -s, -w, -buildid= and -X
// are allowed; -s, -w and -buildid= must each appear exactly once in that
// form. A two-argument -X consumes the next token, also one that looks like
// a flag. Quotes and templates other than simple fields cannot be checked.
func checkLdflags(where string, ldflags []string, f *findings) {
	joined := ldflagsTemplateRE.ReplaceAllString(strings.Join(ldflags, " "), "T")
	if strings.Contains(joined, "{{") || strings.ContainsAny(joined, `'"`) {
		f.add("build-ldflags", "%s: ldflags may only template simple fields such as {{ .Version }} and must not quote", where)
		return
	}
	counts := map[string][]string{}
	tokens := strings.Fields(joined)
	for i := 0; i < len(tokens); i++ {
		token := tokens[i]
		name := flagName(token)
		switch {
		case name == "X" && !strings.Contains(token, "="):
			i++ // the next token is the -X value
		case name == "X":
		case name == "s" || name == "w" || name == "buildid":
			counts[name] = append(counts[name], token)
		default:
			f.add("build-ldflags", "%s: ldflags token %q is not one of -s, -w, -buildid=, -X", where, token)
		}
	}
	for _, rule := range []struct{ name, want string }{{"s", "-s"}, {"w", "-w"}, {"buildid", "-buildid="}} {
		if got := counts[rule.name]; len(got) != 1 || got[0] != rule.want {
			f.add("build-ldflags", "%s: ldflags must contain %s exactly once and no other %s setting, got %q", where, rule.want, rule.name, got)
		}
	}
}

// standardSignArgs are the exact args of the standard signs entry: the
// shared wrapper owns the cosign command line and checks its outputs.
var standardSignArgs = []string{cosignWrapper, "${artifact}", "${certificate}", "${signature}"}

// standardSignKeys are the keys the standard signs entry may carry.
var standardSignKeys = []string{"id", "cmd", "args", "artifacts", "certificate", "signature", "output"}

// isStandardSign reports whether a signs entry is exactly the standard one.
func isStandardSign(m map[string]any) bool {
	for key := range m {
		if !contains(standardSignKeys, key) {
			return false
		}
	}
	if _, ok := m["output"]; ok {
		if _, isBool := m["output"].(bool); !isBool {
			return false
		}
	}
	args := asList(m["args"])
	if len(args) != len(standardSignArgs) {
		return false
	}
	for i, a := range args {
		if s, ok := a.(string); !ok || strings.TrimSpace(s) != standardSignArgs[i] {
			return false
		}
	}
	signature, hasSignature := m["signature"]
	return asString(m["cmd"]) == "sh" &&
		asString(m["artifacts"]) == "checksum" &&
		asString(m["certificate"]) == "${artifact}.pem" &&
		(!hasSignature || asString(signature) == "${artifact}.sig")
}

// checkSigns requires the standard signs entry as the only signs entry. An
// entry over the checksum file (checksum or all) could replace or shadow
// checksums.txt.sig and .pem, and an entry over any other artifacts still
// runs a caller-chosen command after the signing gate, which could replace
// the shared wrapper before the standard entry runs.
func checkSigns(signs []any, f *findings) {
	standard := 0
	for i, s := range signs {
		m := asMap(s)
		switch asString(m["artifacts"]) {
		case "checksum", "all":
		default:
			f.add("caller-hook", "signs[%d] must not be set: only the standard checksum entry may sign, since any signs command runs caller code after the signing gate", i)
			continue
		}
		if isStandardSign(m) {
			standard++
			continue
		}
		f.add("cosign-checksum-sign", "signs[%d] signs the checksum file but is not the standard entry (cmd sh, args %v, certificate ${artifact}.pem)", i, standardSignArgs)
	}
	if standard != 1 {
		f.add("cosign-checksum-sign", "signs must contain exactly one standard entry over artifacts: checksum running %s, found %d", cosignWrapper, standard)
	}
}

// callerCodeKeys are GoReleaser v2.17.0 config sections that run a
// caller-chosen command during a release (custom signing of binaries or
// images, custom publishers, a configurable upx binary). Any entry would run
// caller code after the signing gate with write access to the shared scripts
// and dist/, so the standard admits none of them.
var callerCodeKeys = []string{"binary_signs", "docker_signs", "publishers", "upx"}

// checkCallerCode refuses every config surface that runs caller code after
// the signing gate, besides before.hooks and builds[].hooks (checked where
// they are parsed): the sections above, hooks of universal_binaries and
// dockers_v2, sboms with their own command, args, env or document names, and
// go build's -toolexec (checked with the flags).
func checkCallerCode(doc map[string]any, f *findings) {
	for _, key := range callerCodeKeys {
		if v, ok := doc[key]; ok && v != nil {
			f.add("caller-hook", "%s must not be set: it runs a caller-chosen command after the signing gate", key)
		}
	}
	for _, key := range []string{"universal_binaries", "dockers_v2"} {
		for i, e := range asList(doc[key]) {
			if hooks, ok := asMap(e)["hooks"]; ok && hooks != nil {
				f.add("caller-hook", "%s[%d].hooks must not be set: hooks run caller code after the signing gate", key, i)
			}
		}
	}
	for i, e := range asList(doc["sboms"]) {
		keys := make([]string, 0)
		for key := range asMap(e) {
			if key != "id" && key != "artifacts" {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		for _, key := range keys {
			f.add("caller-hook", "sboms[%d].%s must not be set: the standard SBOM entry runs the pinned syft with its default command and outputs", i, key)
		}
	}
}

// checkEnv refuses a protected name in a KEY=VALUE env list, and any GO*
// name except GOFLAGS in a build env (build is true): a caller-set
// GOTOOLCHAIN, GOPROXY or GOSUMDB could make the go command download and run
// a toolchain the standard did not choose. A name that is not a plain
// identifier (a template, or whitespace around it) or an entry that is not a
// string cannot be checked, so it is refused as well.
func checkEnv(where string, v any, build bool, f *findings) {
	if v == nil {
		return
	}
	list, ok := v.([]any)
	if !ok {
		f.add("protected-env", "%s must be a list of KEY=VALUE strings", where)
		return
	}
	for i, e := range list {
		s, ok := e.(string)
		if !ok {
			f.add("protected-env", "%s[%d] is not a KEY=VALUE string", where, i)
			continue
		}
		// The raw name, untrimmed: " CGO_ENABLED" or "CGO_ENABLED " is a
		// different variable from CGO_ENABLED, so it is refused, not mapped.
		name, _, _ := strings.Cut(s, "=")
		if !envNameRE.MatchString(name) {
			f.add("protected-env", "%s[%d]: name %q is not a plain identifier", where, i, name)
			continue
		}
		if isProtectedEnv(name) {
			f.add("protected-env", "%s[%d] sets %s, which only the reusable workflow may set", where, i, name)
		}
		if strings.HasPrefix(name, "GO") && !(build && name == "GOFLAGS") {
			f.add("protected-env", "%s[%d] sets %s; Go toolchain settings (toolchain download, proxy, checksum database, GOROOT, GOENV) are fixed by the standard", where, i, name)
		}
	}
}

// checkBeforeHooks requires the standard signing gate as the first and only
// before hook, in plain form: a hook that runs earlier could rewrite the
// shared scripts or the environment the gate reads, a later one could
// replace the checksum-signing wrapper after the gate passed, and a gate
// hook with its own env or dir would no longer be the standard gate. Hook env
// is checked for protected names in every position.
func checkBeforeHooks(hooks []any, f *findings) {
	gateAt := -1
	for i, h := range hooks {
		cmd := asString(h)
		if m := asMap(h); m != nil {
			cmd = asString(m["cmd"])
			checkEnv(fmt.Sprintf("before.hooks[%d].env", i), m["env"], false, f)
		}
		if cmd == signingGate && gateAt < 0 {
			gateAt = i
		}
	}
	for i := range hooks {
		if i != gateAt {
			f.add("caller-hook", "before.hooks[%d] must not be set: the standard gate must be the only before hook, since any other hook runs caller code with write access to the shared scripts", i)
		}
	}
	switch {
	case gateAt < 0:
		f.add("signing-gate-hook", "before.hooks must run %s", signingGate)
	case gateAt > 0:
		f.add("signing-gate-hook-order", "before.hooks[0] must be %s; %d hook(s) run before it", signingGate, gateAt)
	default:
		if m := asMap(hooks[0]); m != nil {
			keys := make([]string, 0, len(m))
			for key := range m {
				if key != "cmd" {
					keys = append(keys, key)
				}
			}
			sort.Strings(keys)
			for _, key := range keys {
				f.add("signing-gate-hook-order", "before.hooks[0] must be the plain gate hook, without %s", key)
			}
		}
	}
}

// notarizeWired reports whether one notarize.macos entry is enabled by the
// signing presence and signs and notarizes with the five standard inputs,
// waiting for Apple's verdict.
func notarizeWired(m map[string]any) bool {
	sign := asMap(m["sign"])
	notarize := asMap(m["notarize"])
	wait, _ := notarize["wait"].(bool)
	return asString(m["enabled"]) == notarizeEnable &&
		asString(sign["certificate"]) == "{{ .Env.MACOS_SIGN_P12 }}" &&
		asString(sign["password"]) == "{{ .Env.MACOS_SIGN_P12_PASSWORD }}" &&
		asString(notarize["issuer_id"]) == "{{ .Env.ASC_ISSUER_ID }}" &&
		asString(notarize["key_id"]) == "{{ .Env.ASC_KEY_ID }}" &&
		asString(notarize["key"]) == "{{ .Env.ASC_KEY_P8 }}" &&
		wait
}

// projectName is the config's project_name, or a symbol standing for the
// name GoReleaser derives when it is absent. Build ids and notarize ids both
// default to it, so the symbol compares correctly between them.
func projectName(doc map[string]any) string {
	if name := rawString(doc["project_name"]); name != "" {
		return name
	}
	return "<project_name>"
}

// buildID is a build's id exactly as written (no trimming), defaulting to
// the project name as GoReleaser does.
func buildID(m map[string]any, project string) string {
	if id := rawString(m["id"]); id != "" {
		return id
	}
	return project
}

// releasesDarwin reports whether a build can produce a Darwin binary. It
// fails closed: absent goos (GoReleaser's default includes darwin), a
// templated value, or a special target list such as go_first_class count as
// Darwin. Only a literal skip: true removes a build.
func releasesDarwin(m map[string]any) bool {
	if skip, ok := m["skip"].(bool); ok && skip {
		return false
	}
	if targets := stringList(m["targets"]); len(targets) > 0 {
		for _, target := range targets {
			if strings.HasPrefix(target, "darwin_") || strings.HasPrefix(target, "go_") || strings.Contains(target, "{{") {
				return true
			}
		}
		return false
	}
	goos := stringList(m["goos"])
	if len(goos) == 0 {
		return true
	}
	for _, g := range goos {
		if g == "darwin" || strings.Contains(g, "{{") {
			return true
		}
	}
	return false
}

// checkNotarize requires every notarize.macos entry to be wired to the five
// standard inputs, and every releasable Darwin build id to be selected by a
// wired entry. GoReleaser notarizes only the builds an entry's ids select
// (default: the project name) and skips an empty selection, so an entry that
// names a wrong or partial id set would ship the other Darwin builds unsigned.
func checkNotarize(doc map[string]any, builds []any, f *findings) {
	project := projectName(doc)
	entries := asList(asMap(doc["notarize"])["macos"])
	if len(entries) == 0 {
		f.add("notarize-missing", "notarize.macos needs an entry enabled by %s that signs with MACOS_SIGN_P12/MACOS_SIGN_P12_PASSWORD and notarizes with ASC_ISSUER_ID/ASC_KEY_ID/ASC_KEY_P8 and wait: true", notarizeEnable)
	}
	// GoReleaser selects artifacts by the exact id strings, so ids are
	// compared byte for byte and a whitespace-padded id is refused outright:
	// " tool" would select nothing while looking like "tool".
	padded := func(where, id string) {
		if id != strings.TrimSpace(id) {
			f.add("notarize-uncovered", "%s %q has leading or trailing whitespace; GoReleaser matches ids exactly", where, id)
		}
	}
	padded("project_name", project)
	buildIDs := map[string]bool{}
	for i, b := range builds {
		id := buildID(asMap(b), project)
		padded(fmt.Sprintf("builds[%d].id", i), id)
		buildIDs[id] = true
	}
	covered := map[string]bool{}
	for i, e := range entries {
		m := asMap(e)
		if !notarizeWired(m) {
			f.add("notarize-missing", "notarize.macos[%d] must be enabled by %s, sign with MACOS_SIGN_P12/MACOS_SIGN_P12_PASSWORD, notarize with ASC_ISSUER_ID/ASC_KEY_ID/ASC_KEY_P8 and wait: true", i, notarizeEnable)
			continue
		}
		selected := rawStringList(m["ids"])
		if len(selected) == 0 {
			selected = []string{project}
		}
		for _, id := range selected {
			padded(fmt.Sprintf("notarize.macos[%d].ids", i), id)
			if !buildIDs[id] {
				f.add("notarize-uncovered", "notarize.macos[%d].ids names %q, which is not a build id", i, id)
			}
			covered[id] = true
		}
	}
	for i, b := range builds {
		m := asMap(b)
		if id := buildID(m, project); releasesDarwin(m) && !covered[id] {
			f.add("notarize-uncovered", "builds[%d] (id %q) can produce a Darwin binary but no wired notarize.macos entry selects it", i, id)
		}
	}
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
