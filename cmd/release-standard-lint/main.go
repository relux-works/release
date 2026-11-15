// Command release-standard-lint checks a caller's .goreleaser.yml (and,
// optionally, its release workflow) against the Relux release standard.
//
// Exit 0: no finding. Exit 1: at least one named finding, one per line as
// "<file>: <finding-id>: <message>". Exit 2: usage, read or parse error.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
)

var repositoryRE = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("release-standard-lint", flag.ContinueOnError)
	fs.SetOutput(stderr)
	allow := fs.Bool("allow-placeholders", false, "accept __PLACEHOLDER__ tokens (for the template itself)")
	expectMain := fs.String("main", "", "require a build with this main package")
	expectBinary := fs.String("binary", "", "require a build with this binary name")
	workflow := fs.String("workflow", "", "also lint this caller release workflow")
	repository := fs.String("repository", "", "OWNER/NAME the release must target (required unless -allow-placeholders)")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: release-standard-lint -repository OWNER/NAME [-main PKG] [-binary NAME] [-workflow FILE] .goreleaser.yml")
		fmt.Fprintln(stderr, "       release-standard-lint -allow-placeholders [-workflow FILE] template/.goreleaser.yml")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return 2
	}
	// A caller config is always checked against the repository it releases
	// from; only the template itself (placeholders allowed) has none.
	if *repository == "" && !*allow {
		fmt.Fprintln(stderr, "release-standard-lint: -repository OWNER/NAME is required")
		return 2
	}
	if *repository != "" && !repositoryRE.MatchString(*repository) {
		fmt.Fprintf(stderr, "release-standard-lint: -repository %q is not OWNER/NAME\n", *repository)
		return 2
	}
	opt := Options{AllowPlaceholders: *allow, ExpectMain: *expectMain, ExpectBinary: *expectBinary, ExpectRepository: *repository}

	type target struct {
		path string
		lint func([]byte, Options) ([]Finding, error)
	}
	targets := []target{{fs.Arg(0), LintGoReleaser}}
	if *workflow != "" {
		targets = append(targets, target{*workflow, LintWorkflow})
	}

	total := 0
	for _, t := range targets {
		data, err := os.ReadFile(t.path)
		if err != nil {
			fmt.Fprintf(stderr, "release-standard-lint: %v\n", err)
			return 2
		}
		found, err := t.lint(data, opt)
		if err != nil {
			fmt.Fprintf(stderr, "release-standard-lint: %s: %v\n", t.path, err)
			return 2
		}
		for _, f := range found {
			fmt.Fprintf(stdout, "%s: %s: %s\n", t.path, f.ID, f.Message)
		}
		total += len(found)
	}
	if total > 0 {
		fmt.Fprintf(stderr, "release-standard-lint: %d finding(s)\n", total)
		return 1
	}
	fmt.Fprintln(stdout, "release-standard-lint: ok")
	return 0
}
