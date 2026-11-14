package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

var signingSecrets = []string{"MACOS_SIGN_P12", "MACOS_SIGN_P12_PASSWORD", "ASC_ISSUER_ID", "ASC_KEY_ID", "ASC_KEY_P8"}

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

func stepIndex(steps []any, match func(map[string]any) bool) int {
	for i, s := range steps {
		if match(asMap(s)) {
			return i
		}
	}
	return -1
}

func runContains(text string) func(map[string]any) bool {
	return func(step map[string]any) bool { return strings.Contains(asString(step["run"]), text) }
}

// publishGateProblems checks the publish job's signing admission contract:
// the presence gate and the lint run before GoReleaser, the tree check runs
// immediately before it, the gate reads only the require_signing input and
// the five secrets, GoReleaser gets the scripts path and policy from this
// workflow, and no other step receives a secret.
func publishGateProblems(doc map[string]any) []string {
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	jobs := asMap(doc["jobs"])
	steps := asList(asMap(jobs["publish"])["steps"])
	gate := stepIndex(steps, runContains("/release-standard/scripts/signing-gate.sh"))
	lint := stepIndex(steps, runContains("./cmd/release-standard-lint"))
	tree := stepIndex(steps, runContains("/release-standard/scripts/standard-checkout-check.sh"))
	engine := stepIndex(steps, func(step map[string]any) bool {
		return strings.HasPrefix(asString(step["uses"]), "goreleaser/goreleaser-action@")
	})
	if engine < 0 {
		return []string{"no GoReleaser step in publish"}
	}
	if gate < 0 || gate > engine {
		add("presence gate step (index %d) must run before GoReleaser (index %d)", gate, engine)
	}
	if lint < 0 || lint > engine {
		add("lint step (index %d) must run before GoReleaser (index %d)", lint, engine)
	}
	if tree < 0 || tree != engine-1 {
		add("tree check step (index %d) must be immediately before GoReleaser (index %d)", tree, engine)
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
// config: the real go-release.yml meets the publish contract, and each
// planted violation (gate after GoReleaser, gate removed, gate policy from a
// literal, an extra gate env name, a step between the tree check and
// GoReleaser, a scripts path GoReleaser does not get from the workflow, a
// secret in another step) is reported. Limit: checks the workflow file's
// structure; the hosted run itself is exercised only by a real release.
func TestReusableWorkflowGatesBeforeGoReleaser(t *testing.T) {
	if problems := publishGateProblems(readReusableWorkflow(t)); len(problems) != 0 {
		t.Fatalf("go-release.yml: %v", problems)
	}
	publishSteps := func(doc map[string]any) []any {
		return asList(asMap(asMap(doc["jobs"])["publish"])["steps"])
	}
	setSteps := func(doc map[string]any, steps []any) {
		asMap(asMap(doc["jobs"])["publish"])["steps"] = steps
	}
	find := func(steps []any, text string) int {
		return stepIndex(steps, func(step map[string]any) bool {
			return strings.Contains(asString(step["run"]), text) || strings.HasPrefix(asString(step["uses"]), text)
		})
	}
	plants := map[string]func(doc map[string]any){
		"gate after GoReleaser": func(doc map[string]any) {
			steps := publishSteps(doc)
			g := find(steps, "/scripts/signing-gate.sh")
			gate := steps[g]
			steps = append(steps[:g:g], steps[g+1:]...)
			setSteps(doc, append(steps, gate))
		},
		"gate removed": func(doc map[string]any) {
			steps := publishSteps(doc)
			g := find(steps, "/scripts/signing-gate.sh")
			setSteps(doc, append(steps[:g:g], steps[g+1:]...))
		},
		"gate policy literal": func(doc map[string]any) {
			steps := publishSteps(doc)
			asMap(asMap(steps[find(steps, "/scripts/signing-gate.sh")])["env"])["RELEASE_REQUIRE_SIGNING"] = "false"
		},
		"gate extra env": func(doc map[string]any) {
			steps := publishSteps(doc)
			asMap(asMap(steps[find(steps, "/scripts/signing-gate.sh")])["env"])["RELEASE_STANDARD_SCRIPTS"] = "/tmp"
		},
		"step between tree check and GoReleaser": func(doc map[string]any) {
			steps := publishSteps(doc)
			e := find(steps, "goreleaser/goreleaser-action@")
			extra := map[string]any{"name": "Caller step", "run": "true"}
			out := append(append(append([]any{}, steps[:e]...), extra), steps[e:]...)
			setSteps(doc, out)
		},
		"scripts path from elsewhere": func(doc map[string]any) {
			steps := publishSteps(doc)
			asMap(asMap(steps[find(steps, "goreleaser/goreleaser-action@")])["env"])["RELEASE_STANDARD_SCRIPTS"] = "${{ github.workspace }}/source/scripts"
		},
		"secret in another step": func(doc map[string]any) {
			steps := publishSteps(doc)
			asMap(steps[0])["env"] = map[string]any{"LEAK": "${{ secrets.ASC_KEY_P8 }}"}
		},
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
