# Producer notes: TASK-261113-47fqnb (release standard P0)

Local scratch repository, unsigned commits (the parent re-signs before
publishing). Nothing was pushed, no GitHub repository was created, no secret
was read. Inputs: design v1 and inventory in `.temp/TASK-261113-tns6j3/`, the
launcher at `origin/main` (read-only), curator and a2a-swarm shallow clones.

## Repository name

`relux-works/release`, decided by the owner on 2026-11-14 (over `relux-works/.github`).
Each file names it at most once:

| File | Line | Form |
| --- | --- | --- |
| `.github/workflows/go-release.yml` | 94 | `env.RELEASE_STANDARD_REPOSITORY` |
| `scripts/verify-release.sh` | 27 | default of `RELEASE_STANDARD_REPOSITORY` |
| `cmd/release-standard-lint/lint.go` | 14 | `const standardRepository` |
| `template/release.yml` | 16 | `uses:` line |
| `template/.goreleaser.yml` | 120 | release footer cosign identity regex (dots escaped by hand) |
| `skills/relux-release/SKILL.md` | 13 | `STANDARD_REPO=` |
| `docs/OWNER-GUIDE.md` | 10 | `STANDARD_REPO=` |
| `README.md` | 9 | the naming sentence |
| `NOTES.md` | this section and open item 3 | the list itself |

Not an occurrence: `go.mod` uses the neutral module path
`relux.works/release-standard` on purpose, because a Go module path element
cannot start with a dot (`github.com/relux-works/.github` is invalid). The
module is only built from a checkout (`go run`), never fetched. For a rename to
`.github`, the footer regex needs `\.github`; `verify-release.sh` escapes dots
itself. `linux-repos.yml` does not name the repository (it has no checkout yet).

Other template placeholders: `__BINARY__`, `__MAIN_PACKAGE__`,
`__VERSION_SYMBOL__`, `__HOMEPAGE__`, `__MAINTAINER__`, `__DESCRIPTION__`,
`__LICENSE__`, `__REPOSITORY_NAME__`, `__STANDARD_SHA__`, `__STANDARD_VERSION__`,
`__VERSION_SOURCE_FILE__`, `__VERSION_SOURCE_REGEX__`. The lint reports any
left over as `placeholder-left` unless `-allow-placeholders` is given.

## Decisions

- **`job.workflow_sha`, not `github.job_workflow_sha`.** The brief named
  `github.job_workflow_sha`. That property does not exist: in a called workflow
  `github.*` is the caller's context. GitHub added `job.workflow_sha` (with
  `job.workflow_ref`, `job.workflow_repository`, `job.workflow_file_path`) in
  the August/September 2026 Actions changelog; it is not on GHES. With an
  unknown property the expression is empty and `actions/checkout` would fall
  back to the default branch (fail-open), so each job first refuses unless the
  value is 40 hex (`standard_sha_unavailable`), checks out that SHA, then
  confirms `HEAD` equals it (`standard_sha_mismatch`). actionlint 1.7.12 does
  not know the property yet; `.github/actionlint.yaml` ignores exactly that one
  message, and a control (a typo planted in a copy) showed other errors still fail.
- **Side-by-side checkouts.** Caller at `source/`, standard at
  `release-standard/`. A nested checkout would make GoReleaser's git-dirty check
  fail. GoReleaser runs with `workdir: source` and `--config .goreleaser.yml`
  so it cannot pick a different config (`.config/goreleaser.yml`) than the one
  the lint checked.
- **Gate reads from the tag commit's tree.** `tag-gate.sh` reads the
  allowlist and version source with `git cat-file blob <commit>:<path>` after
  checking tag commit == HEAD, so nothing in the working tree (for example an
  `admission_command`) can change them. With `require_on_main`, the allowlist
  that trusts a tag has been on main. The tag object id is verified, not the
  ref name, to avoid a ref race.
- **Signing gate runs as the GoReleaser before hook**, so secrets reach only
  the GoReleaser step. `RELEASE_REQUIRE_SIGNING` defaults to `true` when unset
  (fail closed); local snapshots set it to `false`. The lint additionally
  requires the notarize block wired to the five secrets, because
  require_signing=true with all five secrets but no notarize block would
  otherwise ship unsigned.
- **Lint runs in admission** against the caller config with the
  `main_package`/`binary` inputs, which gives those inputs a function
  (`build-main-mismatch`, `build-binary-mismatch`). Extra rules beyond the brief
  list, from the design or to close gaps: `notarize-missing`, `sbom-archive`,
  `checksum-name` (verify-release depends on `checksums.txt`), brews/scoops
  `skip-upload`, `placeholder-left`, and the optional `-workflow` check
  (`standard-workflow-unpinned`, `standard-workflow-missing`,
  `goreleaser-unpinned`, which is design §7's "unpinned GoReleaser fails").
- **yaml.v3 is used.** A hand-written YAML subset parser would make the lint a
  false gate on valid configs it misparses. go.sum was produced offline from
  the local module cache with `GOSUMDB=off`; its `h1:` for yaml.v3 v3.0.1
  matches the cache ziphash (`fxVm/GzAz...`). CI downloads it normally.
- **cosign pinned explicitly** to v2.6.1 via `cosign-release:` (the launcher
  relied on installer v3.10.1's default, which the run log shows as v2.6.1).
- **No module cache on release jobs** (`cache: false` on setup-go).
- **Exact version equality for rc tags.** `v1.3.0-rc.1` needs the version
  source to read `1.3.0-rc.1`. See open items.
- **Concurrency** group `relux-go-release-<repo>-<ref>`, distinct from any
  caller group name to avoid the known caller/called same-group deadlock.
- **verify-release.sh** pins the cosign identity to the shared workflow path,
  the caller repo (`--certificate-github-workflow-repository`) and the tag ref
  (`--certificate-github-workflow-ref`); refuses files not in checksums.txt
  unless `--allow-unlisted`. cosign contacts Rekor; `--attest` contacts the GitHub API.
- **Tests** are bash (`set -eu -o pipefail`), compatible with macOS bash 3.2
  (guarded empty-array expansion). Each harness can be pointed at another gate
  (`TAG_GATE`, `SIGNING_GATE`); `harness-control-test.sh` uses that with
  mutants planted in scratch copies and requires a failure on the defeated case.
  The plants were confirmed to apply statically (sed + diff); the controls
  themselves were not run.

## Checks that ran (this host, 2026-11-13)

| Check | Scope | Result |
| --- | --- | --- |
| `sh -n` | `scripts/*.sh` (4) | pass |
| `bash -n` | `tests/*.sh` (6) | pass |
| shellcheck 0.11.0 `-x` | scripts and tests | 0 findings (SC2016 disabled on two intentional literal lines) |
| actionlint 1.7.12 | both workflows and `template/release.yml` | 0 findings (one documented ignore) |
| `go build ./...` | lint | pass (Go 1.25.14, `GOTOOLCHAIN=local`, scratch caches, offline proxy) |
| `go vet ./...` | lint incl. `_test.go` | pass |
| `gofmt -l` | `cmd/` | clean |
| plant applies | both bypass plants, sed on scratch copies | both apply (diff shows the single intended line) |

Logs: `.temp/checks-01.log`, `.temp/go-build-03.log`, `.temp/go-vet-03.log`
(gitignored). **Not run, by instruction:** `tests/*.sh`, `go test`, the lint
CLI itself, and any workflow. Their correctness is unproven until a host that
runs tests executes `make test`.

## Open items

1. Pinned by the parent from the GitHub API (tag peeled to its commit):
   `anchore/sbom-action/download-syft@66cbf4bc1f1c0d2edc94016e65bc221b6bb0ad6c`
   (v0.24.3, released 2026-10-02).
2. `actions/attest-build-provenance@4d101475d8b20a2381f78447822ac1eab6504dd8`
   (v4.2.2, released 2026-08-06).
3. Repository name: decided, `relux-works/release`.
4. `job.workflow_sha` should be confirmed on the first real run (the guard
   fails closed if it is absent). Drop the actionlint ignore once actionlint
   knows the property.
5. rc semantics, DECIDED (parent): exact equality. The version source must read
   `X.Y.Z-rc.N` for an rc tag, so a binary never reports a version other than
   the tag it was released from.
6. curator has no version source file today (version only via ldflags); it
   needs one (or this input made optional) before adopting.
7. GoReleaser `builds.tool` is assumed to accept templates
   (`{{ .Env.RELEASE_STANDARD_SCRIPTS }}/go-toolchain.sh`); hooks certainly do.
   Confirm in the P1 dry run; fallback is setting `GOTOOLCHAIN` in the step env.
8. `attest-build-provenance` with `subject-path` globs that match nothing
   (`*.zip` when windows is off): confirm it only fails when no subject at all
   matches.
9. A missing or expired `TAP_GITHUB_TOKEN` fails the cask upload of a stable
   release after the GitHub release is published. Consider a presence check
   for stable tags.
10. `linux-repos.yml` needs a push token for `relux-works/apt`
    (`APT_REPO_TOKEN`, declared, not in the design) and the actual
    implementation (phase P5).
11. Tap commits stay unsigned bot commits to an unprotected main (design §4).
12. This repository has no CI of its own, no LICENSE, and no release-signers
    file. Add a check workflow running `make check` and `make test` when it is
    published.

## Not done in this task

- `darwin-sign.yml` (phase P3), so cgo/Swift callers (a2a-swarm darwin,
  swarma-keychain, Namecoin) are not covered.
- Any caller adoption (launcher P1, curator P2), the curator footer and a2a
  `pipeline.go:359` identity updates.
- APT/AUR publishing (only the disabled contract).
- Monorepo tags (swarma-chain-lab, agent-walletd).
- Windows/zip and Scoop in the template (curator-only today; the lint accepts
  scoops with `skip_upload: auto`).
- Running any test or workflow.
