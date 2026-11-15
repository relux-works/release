# Design notes

Why the release standard is built the way it is, what it assumes about its
tools, and what it does not cover yet.

## Repository name

`relux-works/release` (owner decision, 2026-11-14, over `relux-works/.github`).
Each file names it at most once:

| File | Form |
| --- | --- |
| `.github/workflows/go-release.yml` | `env.RELEASE_STANDARD_REPOSITORY` |
| `scripts/verify-release.sh` | default of `RELEASE_STANDARD_REPOSITORY` |
| `cmd/release-standard-lint/lint.go` | `const standardRepository` |
| `template/release.yml` | `uses:` line |
| `template/.goreleaser.yml` | release footer cosign identity regex (dots escaped by hand) |
| `skills/relux-release/SKILL.md` | `STANDARD_REPO=` |
| `docs/OWNER-GUIDE.md` | `STANDARD_REPO=` |
| `README.md` | the naming sentence |
| `NOTES.md` | this table |

Not an occurrence: `go.mod` uses the neutral module path
`relux.works/release-standard` on purpose, because a Go module path element
cannot start with a dot (`github.com/relux-works/.github` is invalid). The
module is only built from a checkout (`go run`), never fetched. For a rename to
`.github`, the footer regex needs `\.github`; `verify-release.sh` escapes dots
itself. `linux-repos.yml` does not name the repository (it has no checkout yet).

Template placeholders: `__BINARY__`, `__MAIN_PACKAGE__`, `__VERSION_SYMBOL__`,
`__HOMEPAGE__`, `__MAINTAINER__`, `__DESCRIPTION__`, `__LICENSE__`,
`__REPOSITORY_NAME__`, `__STANDARD_SHA__`, `__STANDARD_VERSION__`,
`__VERSION_SOURCE_FILE__`, `__VERSION_SOURCE_REGEX__`. The lint reports any
left over as `placeholder-left` unless `-allow-placeholders` is given.

## Decisions

- **`job.workflow_sha`, not `github.job_workflow_sha`.** In a called workflow
  `github.*` is the caller's context; `github.job_workflow_sha` does not exist.
  GitHub added `job.workflow_sha` (with `job.workflow_ref`,
  `job.workflow_repository`, `job.workflow_file_path`) in the
  August/September 2026 Actions changelog; it is not on GHES. With an unknown
  property the expression is empty and `actions/checkout` would fall back to
  the default branch (fail-open), so each job first refuses unless the value
  is 40 hex (`standard_sha_unavailable`), checks out that SHA, then confirms
  `HEAD` equals it (`standard_sha_mismatch`). actionlint 1.7.12 does not know
  the property yet; `.github/actionlint.yaml` ignores exactly that one message.
- **Side-by-side checkouts.** Caller at `source/`, standard at
  `release-standard/`. A nested checkout would make GoReleaser's git-dirty check
  fail. GoReleaser runs with `workdir: source` and `--config .goreleaser.yml`
  so it cannot pick a different config (`.config/goreleaser.yml`) than the one
  the lint checked.
- **The tag gate reads from the tag commit's tree.** `tag-gate.sh` reads the
  allowlist and version source with `git cat-file blob <commit>:<path>` after
  checking tag commit == HEAD, so nothing in the working tree (for example an
  `admission_command`) can change them. With `require_on_main`, the allowlist
  that trusts a tag has been on main. The tag object id is verified, not the
  ref name, to avoid a ref race. A tag message carrying OpenPGP or X.509 armor
  (LF or CRLF, well-formed or not) is refused as a prohibited format.
- **The signing gate is authoritative outside GoReleaser.** The publish job
  re-runs the lint, runs `signing-gate.sh` as its own step from the verified
  standard checkout with only `inputs.require_signing` and the five secrets,
  then `standard-checkout-check.sh` compares the checkout's working tree
  (tracked, untracked, ignored, modes) with `job.workflow_sha`'s tree
  immediately before GoReleaser and again right after it. The same gate runs
  as GoReleaser's only `before` hook as defense in depth.
  `RELEASE_REQUIRE_SIGNING` defaults to `true` when unset (fail closed); local
  snapshots set it to `false`. The lint requires the notarize block wired to
  the five secrets and selecting every build that can produce a Darwin binary,
  by exact id, because require_signing=true with all five secrets but no or a
  partial notarize block would otherwise ship unsigned.
- **No caller code after the gate.** The lint refuses every GoReleaser v2.17.0
  surface that runs a caller-chosen command once the gate has passed: later
  `before` hooks, build hooks, `universal_binaries` and `dockers_v2` hooks,
  `binary_signs`, `docker_signs`, `publishers`, `upx`, `signs` other than the
  standard checksum entry, `sboms` with their own command, args, env or
  documents, and `-toolexec`. Checked against GoReleaser v2.17.0
  `pkg/config/config.go`; the `Project` struct has no global `after` hooks, and
  nFPM scripts and Homebrew cask hooks are packaged, not run at release. Caller
  code still runs in admission through `admission_command`, on a separate
  runner without secrets.
- **Effective build settings, byte for byte.** `CGO_ENABLED=0` and
  `GOFLAGS=-mod=readonly` must each be assigned exactly once, `-trimpath` and
  `-buildvcs=false` must be present and not overridden, ldflags must keep
  `-s -w -buildid=`, and per-target `overrides` are refused. Env entries and
  notarize ids are compared without trimming: `CGO_ENABLED=0 ` and
  `' tool'` are different values to the build and to GoReleaser.
- **Protected environment.** No env list may set `RELEASE_REQUIRE_SIGNING`,
  `RELEASE_STANDARD_SCRIPTS`, the five signing names, `TAP_GITHUB_TOKEN`,
  `PATH`, `GITLAB_TOKEN`, `GITEA_TOKEN`, or any `GITHUB_*`, `GORELEASER_*`,
  `ACTIONS_*`, `COSIGN_*` or `SIGSTORE_*` name, nor a `GO*` name other than
  `GOFLAGS` in build env (toolchain download, proxy, checksum database). A
  templated, padded or non-string entry is refused because it cannot be
  checked. GoReleaser applies root env over the process env before it expands
  any hook, so one such entry could redirect the gate, the release token, the
  current tag or where cosign sends the runner's identity.
- **Credentials stay in their place.** The publish step gives GoReleaser the
  release token, the tap token and the five signing inputs, so the lint binds
  each to its one use. The release must name the calling repository under
  `release.github` with `owner` and `name` only (the reusable workflow passes
  `github.repository` as `-repository`; GitHub names compare
  case-insensitively); `release.gitlab`, `release.gitea`, `force_token`,
  `env_files`, `github_urls`, `gitlab_urls` and `gitea_urls` are refused. Every
  cask publishes to `relux-works/homebrew-tap` with exactly
  `{{ .Env.TAP_GITHUB_TOKEN }}` and no other repository key. Every string in
  the config that holds a template is parsed (Go `text/template`) and may read
  the environment only as `.Env.NAME`, `$.Env.NAME`, `isEnvSet "NAME"` or
  `envOrDefault "NAME" ...`, where NAME is a public run fact
  (`GITHUB_REPOSITORY`, `GITHUB_SHA`, `GITHUB_REF_NAME`, `GITHUB_SERVER_URL`,
  `GITHUB_RUN_ID`), `RELEASE_STANDARD_SCRIPTS`, a name the config's own root
  env declares, or the one credential its path may read (the notarize fields
  and the cask token). A bare `.` or `$`, `.Env` without one name after it,
  `index`, an env function with a computed name, `readFile` and
  `mustReadFile`, and a string that does not parse are refused: the lint
  cannot see which variable they read. This proves the read is absent from
  the config text, not what GoReleaser renders.
- **The lint runs in admission and again in publish** against the caller
  config with the `main_package`/`binary` inputs, which gives those inputs a
  function (`build-main-mismatch`, `build-binary-mismatch`). Rules beyond a
  plain template diff: `notarize-missing`, `notarize-uncovered`,
  `sbom-archive`, `checksum-name` (verify-release depends on `checksums.txt`),
  brews/scoops `skip-upload`, `placeholder-left`, and the optional `-workflow`
  check (`standard-workflow-unpinned`, `standard-workflow-missing`,
  `goreleaser-unpinned`). Every finding ID is driven by a negative test, which
  the suite's `TestMain` enforces.
- **yaml.v3 is used.** A hand-written YAML subset parser would make the lint a
  false gate on valid configs it misparses.
- **cosign pinned explicitly** to v2.6.1 via `cosign-release:`. The shared
  wrapper owns the `sign-blob` arguments and refuses unless cosign wrote both
  `checksums.txt.pem` and `checksums.txt.sig` in this run.
- **No module cache on release jobs** (`cache: false` on setup-go).
- **Exact version equality for rc tags.** `v1.3.0-rc.1` needs the version
  source to read `1.3.0-rc.1`, so a binary never reports a version other than
  the tag it was released from.
- **Concurrency** group `relux-go-release-<repo>-<ref>`, distinct from any
  caller group name to avoid the known caller/called same-group deadlock.
- **verify-release.sh** pins the cosign identity to the shared workflow path,
  the caller repo (`--certificate-github-workflow-repository`) and the tag ref
  (`--certificate-github-workflow-ref`); refuses files not in checksums.txt
  unless `--allow-unlisted`. cosign contacts Rekor; `--attest` contacts the GitHub API.
- **Tests** are bash (`set -eu -o pipefail`), compatible with macOS bash 3.2,
  plus the Go suite. Each harness can be pointed at another gate (`TAG_GATE`,
  `SIGNING_GATE`); `harness-control-test.sh` plants mutants and narrowed rules
  in scratch copies and requires the suites to fail on each, so a check that
  stops detecting its violation is caught.

## Validation scope

Hosted CI (`.github/workflows/ci.yml`) runs on every pull request and every
push to main: static checks (`sh -n`, shellcheck, actionlint, `go vet`), the
gate, wrapper, checkout, lint and harness-control suites on Ubuntu and macOS,
and real-engine controls with the pinned GoReleaser v2.17.0 (snapshot builds,
no signing secrets, no publication).

Not exercised by CI: a release through `go-release.yml`, Apple signing and
notarization, keyless cosign signing in a release, provenance attestation,
and publication to GitHub Releases or the tap.

Engine assumptions the engine job checks: `goreleaser build --snapshot` runs
`before` hooks and reports a failing hook's output; `builds.tool` accepts
templates; signs `args` expand templates and `${certificate}`/`${signature}`;
`dist/artifacts.json` lists binaries with `goos`, `goarch`, `path` and
`extra.ID`. The `*-engine-route-live` controls assert that the engine really
lets each planted config bypass the in-engine hook; if a later engine closes a
route, that control fails and should be re-evaluated rather than relaxed.

## Open items

1. `job.workflow_sha` should be confirmed on the first real run (the guard
   fails closed if it is absent). Drop the actionlint ignore once actionlint
   knows the property.
2. curator has no version source file today (version only via ldflags); it
   needs one (or this input made optional) before adopting.
3. `attest-build-provenance` with `subject-path` globs that match nothing
   (`*.zip` when windows is off): confirm it only fails when no subject at all
   matches.
4. A missing or expired `TAP_GITHUB_TOKEN` fails the cask upload of a stable
   release after the GitHub release is published. Consider a presence check
   for stable tags.
5. `linux-repos.yml` needs a push token for `relux-works/apt`
   (`APT_REPO_TOKEN`) and the actual implementation.
6. Tap commits stay unsigned bot commits to an unprotected main.

## Not covered yet

- A Darwin signing workflow for cgo/Swift callers.
- Caller adoption (each caller's own change).
- APT/AUR publishing (only the disabled contract).
- Monorepo tags.
- Windows/zip and Scoop in the template (the lint accepts scoops with
  `skip_upload: auto`).

## Residual limits

The standard bounds what runs before the gate, what the config can set and
which credentials it can name, not everything the caller's own Go code does
at build time; `go build` compiles but does not run it.
