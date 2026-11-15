package main

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	tparse "text/template/parse"
)

// The publish step hands GoReleaser the release token (GITHUB_TOKEN), the
// tap token (TAP_GITHUB_TOKEN) and the five signing inputs. The rules in this
// file keep each credential where the standard puts it: the release goes to
// the caller repository with the default token, the cask goes to the shared
// tap with the tap token, and the signing inputs reach notarize only. A
// caller config that could name a credential anywhere else could hand the
// tap token to the release operation, point a publisher at another
// repository, or print a secret into release notes.

// tapOwner, tapName and tapToken are the only cask repository the standard
// publishes to and the only token it uses there.
const (
	tapOwner = "relux-works"
	tapName  = "homebrew-tap"
	tapToken = "{{ .Env.TAP_GITHUB_TOKEN }}"
)

// tokenSourceKeys are top-level GoReleaser v2.17.0 sections that choose a
// token or the API endpoints a token is sent to. The standard sets none of
// them: GoReleaser reads GITHUB_TOKEN from the workflow and talks to
// github.com.
var tokenSourceKeys = []string{"env_files", "force_token", "github_urls", "gitlab_urls", "gitea_urls"}

// checkReleaseTarget binds the release to the caller repository. release
// must name it under release.github with owner and name only (no token or
// other key), must not name a GitLab or Gitea target, and the top-level
// token-source sections must be absent. When opt.ExpectRepository is set
// (the reusable workflow passes the calling repository), owner/name must
// equal it; GitHub compares both case-insensitively, so this does too.
func checkReleaseTarget(doc map[string]any, opt Options, f *findings) {
	for _, key := range tokenSourceKeys {
		if v, ok := doc[key]; ok && v != nil {
			f.add("release-target", "%s must not be set: it chooses the token or the API a release credential is sent to", key)
		}
	}
	release := asMap(doc["release"])
	for _, key := range []string{"gitlab", "gitea"} {
		if v, ok := release[key]; ok && v != nil {
			f.add("release-target", "release.%s must not be set: the release goes to the caller's GitHub repository", key)
		}
	}
	gh, ok := release["github"].(map[string]any)
	if !ok {
		f.add("release-target", "release.github must name the caller repository with owner and name")
		return
	}
	keys := make([]string, 0, len(gh))
	for key := range gh {
		if key != "owner" && key != "name" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		f.add("release-target", "release.github.%s must not be set: only owner and name are allowed, the release uses the workflow's own token", key)
	}
	owner, ownerOK := gh["owner"].(string)
	name, nameOK := gh["name"].(string)
	if !ownerOK || !nameOK || owner == "" || name == "" {
		f.add("release-target", "release.github.owner and release.github.name must be plain strings")
		return
	}
	if opt.ExpectRepository != "" && !strings.EqualFold(owner+"/"+name, opt.ExpectRepository) {
		f.add("release-target", "release.github is %s/%s, but the release runs for %s", owner, name, opt.ExpectRepository)
	}
}

// checkTapRepository requires every cask to publish to the shared tap with
// the tap token and nothing else in repository: no other owner or name, no
// branch, pull request, git URL or private key, and the token exactly as the
// template writes it.
func checkTapRepository(doc map[string]any, f *findings) {
	for i, c := range asList(doc["homebrew_casks"]) {
		repo, ok := asMap(c)["repository"].(map[string]any)
		if !ok {
			f.add("tap-repository", "homebrew_casks[%d].repository must be %s/%s with token %s", i, tapOwner, tapName, tapToken)
			continue
		}
		keys := make([]string, 0, len(repo))
		for key := range repo {
			if key != "owner" && key != "name" && key != "token" {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		for _, key := range keys {
			f.add("tap-repository", "homebrew_casks[%d].repository.%s must not be set", i, key)
		}
		if rawString(repo["owner"]) != tapOwner || rawString(repo["name"]) != tapName {
			f.add("tap-repository", "homebrew_casks[%d].repository must be %s/%s, got %q/%q", i, tapOwner, tapName, rawString(repo["owner"]), rawString(repo["name"]))
		}
		if rawString(repo["token"]) != tapToken {
			f.add("tap-repository", "homebrew_casks[%d].repository.token must be exactly %s", i, tapToken)
		}
	}
}

// publicEnv are environment names a caller template may read anywhere:
// public facts about the run, and the shared scripts path.
var publicEnv = []string{
	"RELEASE_STANDARD_SCRIPTS",
	"GITHUB_REPOSITORY",
	"GITHUB_SHA",
	"GITHUB_REF_NAME",
	"GITHUB_SERVER_URL",
	"GITHUB_RUN_ID",
}

// credentialPlaces are the only config paths where a template may read a
// credential, and the one name each may read. The values at these paths are
// checked exactly elsewhere (notarizeWired, checkTapRepository).
var credentialPlaces = []struct {
	path *regexp.Regexp
	name string
}{
	{regexp.MustCompile(`^notarize\.macos\[\d+\]\.enabled$`), "MACOS_SIGN_P12"},
	{regexp.MustCompile(`^notarize\.macos\[\d+\]\.sign\.certificate$`), "MACOS_SIGN_P12"},
	{regexp.MustCompile(`^notarize\.macos\[\d+\]\.sign\.password$`), "MACOS_SIGN_P12_PASSWORD"},
	{regexp.MustCompile(`^notarize\.macos\[\d+\]\.notarize\.issuer_id$`), "ASC_ISSUER_ID"},
	{regexp.MustCompile(`^notarize\.macos\[\d+\]\.notarize\.key_id$`), "ASC_KEY_ID"},
	{regexp.MustCompile(`^notarize\.macos\[\d+\]\.notarize\.key$`), "ASC_KEY_P8"},
	{regexp.MustCompile(`^homebrew_casks\[\d+\]\.repository\.token$`), "TAP_GITHUB_TOKEN"},
}

// envFuncs read one environment variable named by their first argument.
var envFuncs = []string{"envOrDefault", "isEnvSet"}

// fileFuncs read a file; /proc/self/environ or a token file would expose
// every credential, so a caller template may not call them.
var fileFuncs = []string{"readFile", "mustReadFile"}

// checkTemplateEnv parses every string in the config that contains a
// template action and refuses any read of the environment except a plain
// .Env.NAME, $.Env.NAME, isEnvSet "NAME" or envOrDefault "NAME" ... of an
// allowed name: a public name, a name the config's own root env declares, or
// the one credential its path may read (credentialPlaces). Any form whose
// name the lint cannot see is refused: bare . or $ (the whole template
// context, Env included), .Env without a single name after it, an env
// function without a string-literal name, and the file-reading functions.
// A string that does not parse as a template is refused as well.
func checkTemplateEnv(doc map[string]any, f *findings) {
	allowed := map[string]bool{}
	for _, name := range publicEnv {
		allowed[name] = true
	}
	for _, e := range asList(doc["env"]) {
		if s, ok := e.(string); ok {
			name, _, _ := strings.Cut(s, "=")
			if envNameRE.MatchString(name) && !isProtectedEnv(name) {
				allowed[name] = true
			}
		}
	}
	walkStrings("", doc, func(path, s string) {
		if !strings.Contains(s, "{{") {
			return
		}
		extra := ""
		for _, p := range credentialPlaces {
			if p.path.MatchString(path) {
				extra = p.name
			}
		}
		ok := func(name string) bool { return allowed[name] || name == extra }
		for _, problem := range templateEnvProblems(s, ok) {
			f.add("env-reference", "%s: %s", path, problem)
		}
	})
}

// walkStrings calls fn for every string scalar under v with its config
// path (a.b[0].c), visiting map keys in sorted order.
func walkStrings(path string, v any, fn func(path, s string)) {
	switch t := v.(type) {
	case string:
		fn(path, t)
	case map[string]any:
		keys := make([]string, 0, len(t))
		for key := range t {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			next := key
			if path != "" {
				next = path + "." + key
			}
			walkStrings(next, t[key], fn)
		}
	case []any:
		for i, e := range t {
			walkStrings(fmt.Sprintf("%s[%d]", path, i), e, fn)
		}
	}
}

// templateEnvProblems parses s as a Go template (GoReleaser's {{ }} syntax)
// and lists every environment read that ok does not admit, or that the
// lint cannot resolve to a single name.
func templateEnvProblems(s string, ok func(name string) bool) []string {
	tree := tparse.New("config")
	tree.Mode = tparse.SkipFuncCheck
	treeSet := map[string]*tparse.Tree{}
	if _, err := tree.Parse(s, "{{", "}}", treeSet); err != nil {
		return []string{fmt.Sprintf("not a parsable template, so its environment reads cannot be checked: %v", err)}
	}
	var problems []string
	add := func(format string, args ...any) { problems = append(problems, fmt.Sprintf(format, args...)) }
	walkTemplate(tree.Root, ok, add)
	names := make([]string, 0, len(treeSet))
	for name := range treeSet {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if t := treeSet[name]; t != nil && t != tree && t.Root != nil {
			walkTemplate(t.Root, ok, add)
		}
	}
	return problems
}

// checkEnvChain checks one field chain (.A.B or $.A.B, idents without the
// leading $): any use of Env must be exactly Env.NAME with an admitted NAME.
func checkEnvChain(idents []string, ok func(string) bool, add func(string, ...any)) {
	for i, id := range idents {
		if id != "Env" {
			continue
		}
		if i != 0 || len(idents) != 2 {
			add("%q reads the environment in a form whose variable name cannot be checked; use .Env.NAME", "."+strings.Join(idents, "."))
			return
		}
		if !ok(idents[1]) {
			add(".Env.%s is not an allowed environment read here", idents[1])
		}
		return
	}
}

func walkTemplate(n tparse.Node, ok func(string) bool, add func(string, ...any)) {
	switch t := n.(type) {
	case nil:
	case *tparse.ListNode:
		if t == nil {
			return
		}
		for _, c := range t.Nodes {
			walkTemplate(c, ok, add)
		}
	case *tparse.ActionNode:
		walkTemplate(t.Pipe, ok, add)
	case *tparse.IfNode:
		walkBranch(&t.BranchNode, ok, add)
	case *tparse.RangeNode:
		walkBranch(&t.BranchNode, ok, add)
	case *tparse.WithNode:
		walkBranch(&t.BranchNode, ok, add)
	case *tparse.TemplateNode:
		walkTemplate(t.Pipe, ok, add)
	case *tparse.PipeNode:
		if t == nil {
			return
		}
		for _, c := range t.Cmds {
			walkTemplate(c, ok, add)
		}
	case *tparse.CommandNode:
		walkCommand(t, ok, add)
	case *tparse.DotNode:
		add("a bare . exposes the whole template context, including Env")
	case *tparse.FieldNode:
		checkEnvChain(t.Ident, ok, add)
	case *tparse.VariableNode:
		if len(t.Ident) == 1 && t.Ident[0] == "$" {
			add("a bare $ exposes the whole template context, including Env")
			return
		}
		// $.Env.NAME, and Env behind any other variable, which the same
		// chain rule refuses unless it is exactly Env.NAME.
		checkEnvChain(t.Ident[1:], ok, add)
	case *tparse.ChainNode:
		walkTemplate(t.Node, ok, add)
		for _, field := range t.Field {
			if field == "Env" {
				add("a chained .Env read cannot be checked; use .Env.NAME")
			}
		}
	case *tparse.IdentifierNode:
		if contains(fileFuncs, t.Ident) {
			add("%s must not be called: a file read can expose credentials", t.Ident)
		}
		if contains(envFuncs, t.Ident) {
			add("%s must be called with a string-literal variable name as its first argument", t.Ident)
		}
	}
}

func walkBranch(b *tparse.BranchNode, ok func(string) bool, add func(string, ...any)) {
	walkTemplate(b.Pipe, ok, add)
	walkTemplate(b.List, ok, add)
	walkTemplate(b.ElseList, ok, add)
}

// walkCommand checks one command. An env function at the head must take a
// string-literal name; every other argument is walked as an operand.
func walkCommand(c *tparse.CommandNode, ok func(string) bool, add func(string, ...any)) {
	args := c.Args
	if len(args) > 0 {
		if id, isIdent := args[0].(*tparse.IdentifierNode); isIdent && contains(envFuncs, id.Ident) {
			if len(args) < 2 {
				add("%s must be called with a string-literal variable name as its first argument", id.Ident)
				return
			}
			lit, isString := args[1].(*tparse.StringNode)
			if !isString {
				add("%s must be called with a string-literal variable name as its first argument", id.Ident)
			} else if !ok(lit.Text) {
				add("%s %q is not an allowed environment read here", id.Ident, lit.Text)
			}
			for _, a := range args[2:] {
				walkTemplate(a, ok, add)
			}
			return
		}
	}
	for _, a := range args {
		walkTemplate(a, ok, add)
	}
}
