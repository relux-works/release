package main

import (
	"strings"
	"testing"
)

const (
	releaseGitHub   = "  github:\n    owner: relux-works\n    name: __REPOSITORY_NAME__\n"
	templateRepo    = "relux-works/__REPOSITORY_NAME__"
	tapRepoBlock    = "      owner: relux-works\n      name: homebrew-tap\n      token: '{{ .Env.TAP_GITHUB_TOKEN }}'\n"
	caskDescription = "    directory: Casks\n    homepage: __HOMEPAGE__\n    description: __DESCRIPTION__\n"
)

// describeCask writes expr, YAML-quoted, as the cask description: a string
// GoReleaser templates and publishes.
func describeCask(t *testing.T, text, expr string) string {
	t.Helper()
	return plant(t, text, caskDescription, "    directory: Casks\n    homepage: __HOMEPAGE__\n    description: '"+strings.ReplaceAll(expr, "'", "''")+"'\n")
}

func lintFor(t *testing.T, text, repository string) []Finding {
	t.Helper()
	found, err := LintGoReleaser([]byte(text), Options{AllowPlaceholders: true, ExpectRepository: repository})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

// Proves the release-target positive: the template, checked for the
// repository it names, has no finding, also when the calling repository
// differs only in letter case (GitHub names are case-insensitive).
func TestReleaseTargetPositive(t *testing.T) {
	template := readTemplate(t, ".goreleaser.yml")
	for _, repo := range []string{templateRepo, "Relux-Works/__repository_name__"} {
		if found := lintFor(t, template, repo); len(found) != 0 {
			t.Fatalf("%s: template has findings: %v", repo, found)
		}
	}
}

// Proves the release is bound to the calling repository and the workflow's
// own token (review r3 P1): a token override under release.github (the tap
// token or a literal), any other key there, a release pointed at the tap or
// another repository, a missing release.github, a GitLab or Gitea target,
// and each top-level section that chooses a token or the API it is sent to
// are all refused with release-target.
func TestReleaseTargetRefused(t *testing.T) {
	template := readTemplate(t, ".goreleaser.yml")
	cases := []struct{ name, old, replacement string }{
		{"tap token override", releaseGitHub, releaseGitHub + "    token: '{{ .Env.TAP_GITHUB_TOKEN }}'\n"},
		{"literal token override", releaseGitHub, releaseGitHub + "    token: abc\n"},
		{"other key", releaseGitHub, releaseGitHub + "    url: https://example.invalid\n"},
		{"release to the tap", releaseGitHub, "  github:\n    owner: relux-works\n    name: homebrew-tap\n"},
		{"release to another owner", releaseGitHub, "  github:\n    owner: someone\n    name: __REPOSITORY_NAME__\n"},
		{"templated name", releaseGitHub, "  github:\n    owner: relux-works\n    name: '{{ .ProjectName }}'\n"},
		{"no release.github", releaseGitHub, ""},
		{"gitlab target", releaseGitHub, releaseGitHub + "  gitlab:\n    owner: relux-works\n    name: x\n"},
		{"gitea target", releaseGitHub, releaseGitHub + "  gitea:\n    owner: relux-works\n    name: x\n"},
		{"force_token", "version: 2\n", "version: 2\nforce_token: github\n"},
		{"env_files", "version: 2\n", "version: 2\nenv_files:\n  github_token: /tmp/token\n"},
		{"github_urls", "version: 2\n", "version: 2\ngithub_urls:\n  api: https://example.invalid/api/v3/\n"},
		{"gitlab_urls", "version: 2\n", "version: 2\ngitlab_urls:\n  api: https://example.invalid/\n"},
		{"gitea_urls", "version: 2\n", "version: 2\ngitea_urls:\n  api: https://example.invalid/\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			found := lintFor(t, plant(t, template, tc.old, tc.replacement), templateRepo)
			if !hasID(found, "release-target") {
				t.Fatalf("want release-target, got %v", ids(found))
			}
		})
	}
}

// Proves every cask publishes only to relux-works/homebrew-tap with the tap
// token as the template writes it: another tap name or owner, another
// token, a padded token, an extra repository key (branch, pull request, git
// URL) and a missing repository are refused with tap-repository. The
// template positive is TestReleaseTargetPositive.
func TestTapRepository(t *testing.T) {
	template := readTemplate(t, ".goreleaser.yml")
	cases := []struct{ name, old, replacement string }{
		{"other tap", "      name: homebrew-tap\n", "      name: other-tap\n"},
		{"other owner", "      owner: relux-works\n      name: homebrew-tap\n", "      owner: someone\n      name: homebrew-tap\n"},
		{"release token", "      token: '{{ .Env.TAP_GITHUB_TOKEN }}'\n", "      token: '{{ .Env.GITHUB_TOKEN }}'\n"},
		{"literal token", "      token: '{{ .Env.TAP_GITHUB_TOKEN }}'\n", "      token: abc\n"},
		{"padded token", "      token: '{{ .Env.TAP_GITHUB_TOKEN }}'\n", "      token: ' {{ .Env.TAP_GITHUB_TOKEN }}'\n"},
		{"branch", tapRepoBlock, tapRepoBlock + "      branch: other\n"},
		{"pull request", tapRepoBlock, tapRepoBlock + "      pull_request:\n        enabled: true\n"},
		{"git url", tapRepoBlock, tapRepoBlock + "      git:\n        url: git@example.invalid:x/y.git\n"},
		{"no repository", "    repository:\n" + tapRepoBlock, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			found := lintFor(t, plant(t, template, tc.old, tc.replacement), templateRepo)
			if !hasID(found, "tap-repository") {
				t.Fatalf("want tap-repository, got %v", ids(found))
			}
		})
	}
}

// Proves a caller template can read a credential only at its one canonical
// place. Each case writes the expression into the cask description (a
// string GoReleaser templates and publishes) and must be refused with
// env-reference: a direct read of each credential, of the runner's OIDC
// request token or of an undeclared name; $.Env; every form whose name the
// lint cannot see (index, a bare . or $, .Env as a whole, with/range over
// .Env, a parenthesized .Env, an env function with a computed or piped name,
// a read hidden behind a "}}" string literal, a define or template call);
// the file-reading functions; and a string that does not parse. Limit: the
// lint proves the read is absent from the config text, not what GoReleaser
// would render.
func TestTemplateEnvRefused(t *testing.T) {
	template := readTemplate(t, ".goreleaser.yml")
	exprs := []string{
		`{{ .Env.TAP_GITHUB_TOKEN }}`,
		`{{ .Env.GITHUB_TOKEN }}`,
		`{{ .Env.MACOS_SIGN_P12 }}`,
		`{{ .Env.MACOS_SIGN_P12_PASSWORD }}`,
		`{{ .Env.ASC_ISSUER_ID }}`,
		`{{ .Env.ASC_KEY_ID }}`,
		`{{ .Env.ASC_KEY_P8 }}`,
		`{{ .Env.ACTIONS_ID_TOKEN_REQUEST_TOKEN }}`,
		`{{ .Env.UNDECLARED }}`,
		`{{ $.Env.ASC_KEY_P8 }}`,
		`{{ index .Env "TAP_GITHUB_TOKEN" }}`,
		`{{ index . "Env" }}`,
		`{{ . }}`,
		`{{ $ }}`,
		`{{ printf "%v" .Env }}`,
		`{{ with .Env }}{{ .TAP_GITHUB_TOKEN }}{{ end }}`,
		`{{ range $k, $v := .Env }}{{ $v }}{{ end }}`,
		`{{ (.Env).TAP_GITHUB_TOKEN }}`,
		`{{ .Env.TAP_GITHUB_TOKEN.X }}`,
		`{{ envOrDefault "TAP_GITHUB_TOKEN" "" }}`,
		`{{ envOrDefault (print "TAP_GITHUB" "_TOKEN") "" }}`,
		`{{ "TAP_GITHUB_TOKEN" | isEnvSet }}`,
		`{{ isEnvSet "ASC_KEY_P8" }}`,
		`{{ "}}" }}{{ .Env.TAP_GITHUB_TOKEN }}`,
		`{{ define "x" }}{{ .Env.ASC_KEY_P8 }}{{ end }}ok`,
		`{{ template "x" . }}`,
		`{{ readFile "/proc/self/environ" }}`,
		`{{ mustReadFile "/tmp/token" }}`,
		`{{ .Env.TAP_GITHUB_TOKEN`,
	}
	for _, expr := range exprs {
		t.Run(expr, func(t *testing.T) {
			found := lintFor(t, describeCask(t, template, expr), templateRepo)
			if !hasID(found, "env-reference") {
				t.Fatalf("want env-reference, got %v", ids(found))
			}
		})
	}
}

// Proves the credential places are exact: a credential read at the wrong
// canonical-looking place (the App Store key as the certificate password),
// a credential relayed through a root env entry, and a protected name
// declared in root env and then read are each refused with env-reference.
func TestTemplateEnvRelayRefused(t *testing.T) {
	template := readTemplate(t, ".goreleaser.yml")
	cases := map[string]func() string{
		"wrong credential place": func() string {
			return plant(t, template, "password: '{{ .Env.MACOS_SIGN_P12_PASSWORD }}'", "password: '{{ .Env.ASC_KEY_P8 }}'")
		},
		"root env relay": func() string {
			return plant(t, template, "version: 2\n", "version: 2\nenv:\n  - 'RELAY={{ .Env.TAP_GITHUB_TOKEN }}'\n")
		},
		"declared protected name": func() string {
			text := plant(t, template, "version: 2\n", "version: 2\nenv:\n  - GITHUB_TOKEN=x\n")
			return describeCask(t, text, "{{ .Env.GITHUB_TOKEN }}")
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			if found := lintFor(t, build(), templateRepo); !hasID(found, "env-reference") {
				t.Fatalf("want env-reference, got %v", ids(found))
			}
		})
	}
}

// Proves the positive side of the env-reference rule: public run facts, a
// name the config's own root env declares (read directly, with $, with
// isEnvSet or envOrDefault), ordinary template fields and control flow, and
// text that only looks like Env are accepted with no finding at all.
func TestTemplateEnvAccepted(t *testing.T) {
	template := readTemplate(t, ".goreleaser.yml")
	template = plant(t, template, "version: 2\n", "version: 2\nenv:\n  - FIXTURE_NOTE=hello\n")
	exprs := []string{
		`{{ .Env.GITHUB_REPOSITORY }} at {{ .Env.GITHUB_SHA }}`,
		`{{ $.Env.GITHUB_REF_NAME }}`,
		`{{ .Env.FIXTURE_NOTE }}`,
		`{{ isEnvSet "FIXTURE_NOTE" }}`,
		`{{ envOrDefault "FIXTURE_NOTE" "none" }}`,
		`{{ .ProjectName }} {{ .Version }}`,
		`{{ if .IsSnapshot }}snapshot{{ else }}{{ .Tag }}{{ end }}`,
		`Environment and Envoy, no template`,
	}
	for _, expr := range exprs {
		t.Run(expr, func(t *testing.T) {
			if found := lintFor(t, describeCask(t, template, expr), templateRepo); len(found) != 0 {
				t.Fatalf("want no finding, got %v", found)
			}
		})
	}
}
