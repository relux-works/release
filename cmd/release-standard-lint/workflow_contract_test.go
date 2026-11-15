package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

var signingSecrets = []string{"MACOS_SIGN_P12", "MACOS_SIGN_P12_PASSWORD", "ASC_ISSUER_ID", "ASC_KEY_ID", "ASC_KEY_P8"}

const (
	stepGate     = "Signing presence gate"
	stepTreePre  = "Confirm the standard scripts are unmodified"
	stepTreePost = "Confirm GoReleaser left the standard scripts unmodified"
	stepLint     = "Lint the caller GoReleaser config against the standard"
	stepCommit   = "Confirm the standard checkout commit"
)

// gateSpec pins one authoritative gate step: its exact body (compared with
// whitespace runs collapsed), its working directory and its allowed keys.
// Any other key, notably if, continue-on-error, shell or timeout-minutes,
// could disable, soften or redirect the gate and is a contract violation.
type gateSpec struct {
	job, name, workingDir, run string
}

var gateSpecs = []gateSpec{
	{"admission", stepCommit, "", `[ "$(git -C "$GITHUB_WORKSPACE/release-standard" rev-parse HEAD)" = "$WORKFLOW_SHA" ] || {
  echo '::error::REFUSED[standard_sha_mismatch]: release-standard checkout is not job.workflow_sha'
  exit 1
}`},
	{"admission", stepLint, "release-standard", `go run ./cmd/release-standard-lint \
  -repository "$EXPECT_REPOSITORY" \
  -main "$EXPECT_MAIN" -binary "$EXPECT_BINARY" \
  "$GITHUB_WORKSPACE/source/.goreleaser.yml"`},
	{"publish", stepCommit, "", `[ "$(git -C "$GITHUB_WORKSPACE/release-standard" rev-parse HEAD)" = "$WORKFLOW_SHA" ] || {
  echo '::error::REFUSED[standard_sha_mismatch]: release-standard checkout is not job.workflow_sha'
  exit 1
}`},
	{"publish", stepLint, "release-standard", `go run ./cmd/release-standard-lint \
  -repository "$EXPECT_REPOSITORY" \
  -main "$EXPECT_MAIN" -binary "$EXPECT_BINARY" \
  "$GITHUB_WORKSPACE/source/.goreleaser.yml"`},
	{"publish", stepGate, "", `"$GITHUB_WORKSPACE/release-standard/scripts/signing-gate.sh"`},
	{"publish", stepTreePre, "", `"$GITHUB_WORKSPACE/release-standard/scripts/standard-checkout-check.sh" \
  "$GITHUB_WORKSPACE/release-standard" "$WORKFLOW_SHA"`},
	{"publish", stepTreePost, "", `"$GITHUB_WORKSPACE/release-standard/scripts/standard-checkout-check.sh" \
  "$GITHUB_WORKSPACE/release-standard" "$WORKFLOW_SHA"`},
}

func readReusableWorkflow(t *testing.T) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "go-release.yml"))
	if err != nil {
		t.Fatalf("read go-release.yml: %v", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("parse go-release.yml: %v", err)
	}
	return doc
}

func jobSteps(doc map[string]any, job string) []any {
	return asList(asMap(asMap(doc["jobs"])[job])["steps"])
}

// stepNamed returns the index of the only step with this name, or -1 when
// there is none or more than one.
func stepNamed(steps []any, name string) int {
	at := -1
	for i, s := range steps {
		if asString(asMap(s)["name"]) == name {
			if at >= 0 {
				return -1
			}
			at = i
		}
	}
	return at
}

func normalizeBody(s string) string { return strings.Join(strings.Fields(s), " ") }

// gateStepProblems checks each gateSpec step: present exactly once, only
// name/env/run (and the pinned working-directory) as keys, and the exact
// body. Neither job may carry if or continue-on-error.
func gateStepProblems(doc map[string]any) []string {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	jobs := asMap(doc["jobs"])
	for _, job := range []string{"admission", "publish"} {
		for _, key := range []string{"if", "continue-on-error"} {
			if _, ok := asMap(jobs[job])[key]; ok {
				add("jobs.%s sets %s", job, key)
			}
		}
	}
	for _, spec := range gateSpecs {
		steps := jobSteps(doc, spec.job)
		i := stepNamed(steps, spec.name)
		if i < 0 {
			add("jobs.%s: step %q missing or duplicated", spec.job, spec.name)
			continue
		}
		step := asMap(steps[i])
		for key := range step {
			switch key {
			case "name", "env", "run":
			case "working-directory":
				if spec.workingDir == "" {
					add("jobs.%s %q sets working-directory", spec.job, spec.name)
				}
			default:
				add("jobs.%s %q sets %s; an authoritative gate step must run unconditionally and fail the job", spec.job, spec.name, key)
			}
		}
		if got := rawString(step["working-directory"]); got != spec.workingDir {
			add("jobs.%s %q working-directory = %q, want %q", spec.job, spec.name, got, spec.workingDir)
		}
		if normalizeBody(rawString(step["run"])) != normalizeBody(spec.run) {
			add("jobs.%s %q run body differs from the pinned gate body", spec.job, spec.name)
		}
	}
	return problems
}

// publishGateProblems checks the publish job's signing admission contract:
// the presence gate and the lint run before GoReleaser, the tree checks run
// immediately before and immediately after it, the gate reads only the
// require_signing input and the five secrets, GoReleaser gets the scripts
// path and policy from this workflow, no other step receives a secret, and
// every authoritative gate step meets its gateSpec.
func publishGateProblems(doc map[string]any) []string {
	problems := gateStepProblems(doc)
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	jobs := asMap(doc["jobs"])
	steps := jobSteps(doc, "publish")
	gate := stepNamed(steps, stepGate)
	lint := stepNamed(steps, stepLint)
	pre := stepNamed(steps, stepTreePre)
	post := stepNamed(steps, stepTreePost)
	engine := -1
	for i, s := range steps {
		if strings.HasPrefix(asString(asMap(s)["uses"]), "goreleaser/goreleaser-action@") {
			engine = i
		}
	}
	if engine < 0 {
		return append(problems, "no GoReleaser step in publish")
	}
	if gate < 0 || gate > engine {
		add("presence gate step (index %d) must run before GoReleaser (index %d)", gate, engine)
	}
	if lint < 0 || lint > engine {
		add("lint step (index %d) must run before GoReleaser (index %d)", lint, engine)
	}
	if pre < 0 || pre != engine-1 {
		add("pre tree check step (index %d) must be immediately before GoReleaser (index %d)", pre, engine)
	}
	if post < 0 || post != engine+1 {
		add("post tree check step (index %d) must be immediately after GoReleaser (index %d)", post, engine)
	}
	if gate >= 0 {
		env := asMap(asMap(steps[gate])["env"])
		want := map[string]string{"RELEASE_REQUIRE_SIGNING": "${{ inputs.require_signing }}"}
		for _, name := range signingSecrets {
			want[name] = "${{ secrets." + name + " }}"
		}
		if len(env) != len(want) {
			add("presence gate env has %d entries, want exactly %d", len(env), len(want))
		}
		for name, value := range want {
			if got := asString(env[name]); got != value {
				add("presence gate env %s = %q, want %q", name, got, value)
			}
		}
	}
	engineEnv := asMap(asMap(steps[engine])["env"])
	for name, value := range map[string]string{
		"RELEASE_STANDARD_SCRIPTS": "${{ github.workspace }}/release-standard/scripts",
		"RELEASE_REQUIRE_SIGNING":  "${{ inputs.require_signing }}",
	} {
		if got := asString(engineEnv[name]); got != value {
			add("GoReleaser env %s = %q, want %q", name, got, value)
		}
	}
	for jobName, job := range jobs {
		for i, s := range asList(asMap(job)["steps"]) {
			if jobName == "publish" && (i == gate || i == engine) {
				continue
			}
			text, _ := yaml.Marshal(s)
			if strings.Contains(string(text), "secrets.") {
				add("jobs.%s.steps[%d] receives a secret", jobName, i)
			}
		}
	}
	return problems
}

// Proves the reusable workflow enforces signing admission outside the caller
// config and that its authoritative gates cannot be quietly disabled: the
// real go-release.yml meets the contract, and each planted violation is
// reported: gate after GoReleaser or removed, gate policy from a literal, an
// extra gate env name, a step between a tree check and GoReleaser, the post
// check removed, a scripts path from elsewhere, a secret in another step, and
// (r2) if: false, a conditional if, continue-on-error, a shell override and
// token-preserving no-op bodies on the gate, tree-check and lint steps.
// Limit: checks the workflow file's structure; TestSavedGateStepsExecute
// runs the saved bodies, and only a real release exercises the hosted run.
func TestReusableWorkflowGatesBeforeGoReleaser(t *testing.T) {
	if problems := publishGateProblems(readReusableWorkflow(t)); len(problems) != 0 {
		t.Fatalf("go-release.yml: %v", problems)
	}
	publish := func(doc map[string]any) []any { return jobSteps(doc, "publish") }
	setPublish := func(doc map[string]any, steps []any) {
		asMap(asMap(doc["jobs"])["publish"])["steps"] = steps
	}
	named := func(doc map[string]any, job, name string) map[string]any {
		return asMap(jobSteps(doc, job)[stepNamed(jobSteps(doc, job), name)])
	}
	engineAt := func(steps []any) int {
		for i, s := range steps {
			if strings.HasPrefix(asString(asMap(s)["uses"]), "goreleaser/goreleaser-action@") {
				return i
			}
		}
		return -1
	}
	remove := func(doc map[string]any, name string) {
		steps := publish(doc)
		g := stepNamed(steps, name)
		setPublish(doc, append(steps[:g:g], steps[g+1:]...))
	}
	insertAt := func(doc map[string]any, at int) {
		steps := publish(doc)
		extra := map[string]any{"name": "Caller step", "run": "true"}
		setPublish(doc, append(append(append([]any{}, steps[:at]...), extra), steps[at:]...))
	}
	plants := map[string]func(doc map[string]any){
		"gate after GoReleaser": func(doc map[string]any) {
			steps := publish(doc)
			g := stepNamed(steps, stepGate)
			gate := steps[g]
			setPublish(doc, append(append(steps[:g:g], steps[g+1:]...), gate))
		},
		"gate removed":       func(doc map[string]any) { remove(doc, stepGate) },
		"post check removed": func(doc map[string]any) { remove(doc, stepTreePost) },
		"gate policy literal": func(doc map[string]any) {
			asMap(named(doc, "publish", stepGate)["env"])["RELEASE_REQUIRE_SIGNING"] = "false"
		},
		"gate extra env": func(doc map[string]any) {
			asMap(named(doc, "publish", stepGate)["env"])["RELEASE_STANDARD_SCRIPTS"] = "/tmp"
		},
		"step between pre check and GoReleaser": func(doc map[string]any) { insertAt(doc, engineAt(publish(doc))) },
		"step between GoReleaser and post check": func(doc map[string]any) {
			insertAt(doc, engineAt(publish(doc))+1)
		},
		"scripts path from elsewhere": func(doc map[string]any) {
			steps := publish(doc)
			asMap(asMap(steps[engineAt(steps)])["env"])["RELEASE_STANDARD_SCRIPTS"] = "${{ github.workspace }}/source/scripts"
		},
		"secret in another step": func(doc map[string]any) {
			asMap(publish(doc)[0])["env"] = map[string]any{"LEAK": "${{ secrets.ASC_KEY_P8 }}"}
		},
		"publish job continue-on-error": func(doc map[string]any) {
			asMap(asMap(doc["jobs"])["publish"])["continue-on-error"] = true
		},
	}
	for _, spec := range gateSpecs {
		spec := spec
		label := spec.job + " " + spec.name
		plants[label+": if false"] = func(doc map[string]any) { named(doc, spec.job, spec.name)["if"] = false }
		plants[label+": conditional if"] = func(doc map[string]any) {
			named(doc, spec.job, spec.name)["if"] = "${{ inputs.require_signing }}"
		}
		plants[label+": continue-on-error"] = func(doc map[string]any) {
			named(doc, spec.job, spec.name)["continue-on-error"] = true
		}
		plants[label+": shell override"] = func(doc map[string]any) {
			named(doc, spec.job, spec.name)["shell"] = "true {0}"
		}
		plants[label+": no-op body"] = func(doc map[string]any) {
			step := named(doc, spec.job, spec.name)
			step["run"] = ": " + rawString(step["run"])
		}
		plants[label+": echoed body"] = func(doc map[string]any) {
			step := named(doc, spec.job, spec.name)
			step["run"] = "echo " + rawString(step["run"]) + " || true"
		}
	}
	for name, plantFn := range plants {
		t.Run(name, func(t *testing.T) {
			doc := readReusableWorkflow(t)
			plantFn(doc)
			if problems := publishGateProblems(doc); len(problems) == 0 {
				t.Fatalf("planted violation not reported")
			}
		})
	}
}

var expressionRE = regexp.MustCompile(`^\$\{\{\s*([^}]*?)\s*\}\}$`)

// runSavedStep runs one saved workflow step the way a GitHub-hosted runner
// does (bash --noprofile --norc -eo pipefail, GITHUB_WORKSPACE, the step's
// env with each ${{ expression }} resolved from values, its
// working-directory). A step with an if is treated as skipped and one with
// continue-on-error as never failing the job, so a disabled gate shows up as
// a missing refusal. It returns the effective exit code and the output.
func runSavedStep(t *testing.T, step map[string]any, workspace string, values map[string]string) (int, string) {
	t.Helper()
	if _, ok := step["shell"]; ok {
		t.Fatalf("step %q overrides shell; refusing to emulate it", asString(step["name"]))
	}
	if _, ok := step["if"]; ok {
		return 0, "skipped: step has an if condition"
	}
	env := []string{}
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if contains(signingSecrets, name) || strings.HasPrefix(name, "RELEASE_") || strings.HasPrefix(name, "GITHUB_") ||
			name == "BASH_ENV" || name == "ENV" {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "GITHUB_WORKSPACE="+workspace)
	for name, raw := range asMap(step["env"]) {
		value := fmt.Sprint(raw)
		if m := expressionRE.FindStringSubmatch(value); m != nil {
			v, ok := values[m[1]]
			if !ok {
				t.Fatalf("step %q: no test value for expression %q", asString(step["name"]), m[1])
			}
			value = v
		}
		env = append(env, name+"="+value)
	}
	cmd := exec.Command("bash", "--noprofile", "--norc", "-eo", "pipefail", "-c", rawString(step["run"]))
	cmd.Env = env
	cmd.Dir = filepath.Join(workspace, rawString(step["working-directory"]))
	out, err := cmd.CombinedOutput()
	rc := 0
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		rc = exitErr.ExitCode()
	} else if err != nil {
		t.Fatalf("run step %q: %v", asString(step["name"]), err)
	}
	if b, _ := step["continue-on-error"].(bool); b {
		rc = 0
	}
	return rc, string(out)
}

// gitFixture creates a committed git repository holding scripts/ (copied from
// this repository) and returns its directory and commit.
func gitFixture(t *testing.T, dir string) string {
	t.Helper()
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("cp", "-R", filepath.Join(repoRoot, "scripts"), dir).CombinedOutput(); err != nil {
		t.Fatalf("copy scripts: %v: %s", err, out)
	}
	git("init", "-q")
	git("add", ".")
	git("commit", "-q", "-m", "fixture")
	return git("rev-parse", "HEAD")
}

// Proves the saved authoritative steps of go-release.yml actually enforce
// when run as a runner runs them, so a step that is skipped, softened or
// emptied cannot pass: the presence gate refuses require_signing=true with
// no secret and accepts false (unsigned) and all five dummies (signed); both
// tree checks pass on the committed fixture and refuse a modified file and
// another SHA; both commit checks accept the right SHA and refuse another;
// both lint steps pass a filled template and refuse a drifted one. Dummy
// values only. Limit: GitHub expression evaluation is emulated for the
// expressions these steps use; the hosted runner itself is not involved.
func TestSavedGateStepsExecute(t *testing.T) {
	doc := readReusableWorkflow(t)
	step := func(job, name string) map[string]any {
		steps := jobSteps(doc, job)
		i := stepNamed(steps, name)
		if i < 0 {
			t.Fatalf("jobs.%s: no single step %q", job, name)
		}
		return asMap(steps[i])
	}
	expect := func(label string, rc int, out string, wantRC int, want string) {
		t.Helper()
		if rc != wantRC || !strings.Contains(out, want) {
			t.Errorf("%s: want rc=%d and %q, got rc=%d: %s", label, wantRC, want, rc, out)
		}
	}

	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	standardWS := t.TempDir()
	if err := os.Symlink(repoRoot, filepath.Join(standardWS, "release-standard")); err != nil {
		t.Fatal(err)
	}

	gateValues := func(require string, secret string) map[string]string {
		v := map[string]string{"inputs.require_signing": require}
		for _, name := range signingSecrets {
			v["secrets."+name] = secret
		}
		return v
	}
	gate := step("publish", stepGate)
	rc, out := runSavedStep(t, gate, standardWS, gateValues("true", ""))
	expect("gate required, absent", rc, out, 4, "REFUSED[signing_required_but_absent]")
	rc, out = runSavedStep(t, gate, standardWS, gateValues("false", ""))
	expect("gate not required, absent", rc, out, 0, "signing-gate: unsigned")
	rc, out = runSavedStep(t, gate, standardWS, gateValues("true", "dummy-not-a-secret"))
	expect("gate required, all five", rc, out, 0, "signing-gate: signed")

	treeWS := t.TempDir()
	fixture := filepath.Join(treeWS, "release-standard")
	sha := gitFixture(t, fixture)
	other := strings.Repeat("0", 40)
	for _, job := range []string{"admission", "publish"} {
		commit := step(job, stepCommit)
		rc, out = runSavedStep(t, commit, treeWS, map[string]string{"steps.standard.outputs.sha": sha})
		expect(job+" commit check, right sha", rc, out, 0, "")
		rc, out = runSavedStep(t, commit, treeWS, map[string]string{"steps.standard.outputs.sha": other})
		expect(job+" commit check, other sha", rc, out, 1, "REFUSED[standard_sha_mismatch]")
	}
	for _, name := range []string{stepTreePre, stepTreePost} {
		check := step("publish", name)
		rc, out = runSavedStep(t, check, treeWS, map[string]string{"steps.standard.outputs.sha": sha})
		expect(name+", clean", rc, out, 0, "standard-checkout-check: ")
		rc, out = runSavedStep(t, check, treeWS, map[string]string{"steps.standard.outputs.sha": other})
		expect(name+", other sha", rc, out, 30, "REFUSED[standard_sha_mismatch]")
		wrapper := filepath.Join(fixture, "scripts", "cosign-sign-checksum.sh")
		original, err := os.ReadFile(wrapper)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(wrapper, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		rc, out = runSavedStep(t, check, treeWS, map[string]string{"steps.standard.outputs.sha": sha})
		expect(name+", replaced wrapper", rc, out, 31, "REFUSED[standard_tree_modified]")
		if err := os.WriteFile(wrapper, original, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	lintWS := standardWS
	if err := os.MkdirAll(filepath.Join(lintWS, "source"), 0o755); err != nil {
		t.Fatal(err)
	}
	filled := strings.NewReplacer("__MAIN_PACKAGE__", "./cmd/fixture", "__VERSION_SYMBOL__", "main.version",
		"__HOMEPAGE__", "https://example.invalid/fixture", "__MAINTAINER__", "Fixture <fixture@example.invalid>").
		Replace(readTemplate(t, ".goreleaser.yml"))
	filled = placeholderRE.ReplaceAllString(filled, "fixture")
	lintValues := map[string]string{"inputs.main_package": "./cmd/fixture", "inputs.binary": "fixture", "github.repository": "relux-works/fixture"}
	otherRepo := map[string]string{"inputs.main_package": "./cmd/fixture", "inputs.binary": "fixture", "github.repository": "relux-works/homebrew-tap"}
	config := filepath.Join(lintWS, "source", ".goreleaser.yml")
	for _, job := range []string{"admission", "publish"} {
		lint := step(job, stepLint)
		if err := os.WriteFile(config, []byte(filled), 0o600); err != nil {
			t.Fatal(err)
		}
		rc, out = runSavedStep(t, lint, lintWS, lintValues)
		expect(job+" lint, filled template", rc, out, 0, "release-standard-lint: ok")
		if err := os.WriteFile(config, []byte(strings.Replace(filled, "prerelease: auto", "prerelease: false", 1)), 0o600); err != nil {
			t.Fatal(err)
		}
		rc, out = runSavedStep(t, lint, lintWS, lintValues)
		expect(job+" lint, drifted config", rc, out, 1, ": release-prerelease: ")
		// The step passes the calling repository: the same filled config
		// run for another repository is refused on its release target.
		if err := os.WriteFile(config, []byte(filled), 0o600); err != nil {
			t.Fatal(err)
		}
		rc, out = runSavedStep(t, lint, lintWS, otherRepo)
		expect(job+" lint, other calling repository", rc, out, 1, ": release-target: ")
	}
}
