# Relux release standard

Shared release machinery for Relux Works Go binaries: one reusable GitHub
Actions workflow, the admission gates, a GoReleaser template, a config lint,
an agent skill and an owner guide. Callers keep only their per-binary
`.goreleaser.yml` and a short `release.yml`; this repository owns the tool
versions, the gates and the publishing steps.

The repository is `relux-works/release` (owner decision, 2026-11-14). Every file
names it once; NOTES.md lists each place.

## Status

Not published yet. Hosted CI (`.github/workflows/ci.yml`: static checks,
Ubuntu and macOS tests, and the real-engine job with pinned GoReleaser
v2.17.0) runs on every pull request and was green on `66b5f07` (review r2).
No release has gone through `go-release.yml` yet, so its hosted run, Apple
notarization and publication are unproven. Changes after `66b5f07` are
statically checked until CI runs them; NOTES.md records which.
`linux-repos.yml` is disabled and unimplemented by design.

## Parts

| Part | Path | What it does |
| --- | --- | --- |
| Go release workflow | `.github/workflows/go-release.yml` | Reusable `workflow_call`: admission (tag gate, lint, optional caller command) then publish (GoReleaser v2.17.0, cosign, syft, notarization, provenance) |
| Linux repositories | `.github/workflows/linux-repos.yml` | Reusable, **disabled**: APT index and AUR `-bin` contract with failing stubs |
| Tag gate | `scripts/tag-gate.sh` | Annotated, SSH-signed, allowlisted tag equal to the ref, HEAD and the version source; optional on-main and prerelease rules; named refusals with distinct exit codes |
| Signing gate | `scripts/signing-gate.sh` | All-or-none presence of the five darwin signing secrets, with `require_signing`; runs as its own publish step before GoReleaser, and as GoReleaser's first hook |
| Checksum signing | `scripts/cosign-sign-checksum.sh` | Keyless `cosign sign-blob` over checksums.txt; refuses unless `.pem` and `.sig` were written |
| Checkout check | `scripts/standard-checkout-check.sh` | The standard checkout's working tree equals `job.workflow_sha`, right before GoReleaser |
| Toolchain | `scripts/go-toolchain.sh` | GoReleaser `tool`: GOTOOLCHAIN from the go.mod `go` line |
| Consumer verification | `scripts/verify-release.sh` | cosign identity of the shared workflow, `sha256sum -c`, optional `gh attestation verify` |
| Lint | `cmd/release-standard-lint` | Checks a caller `.goreleaser.yml` (and optionally its workflow) against the template |
| Templates | `template/.goreleaser.yml`, `template/release.yml` | What a caller copies |
| Tests | `tests/` | Gate, signing, wrapper, checkout, lint, harness-control and real-engine tests (throwaway git, ssh-keygen and GoReleaser fixtures) |
| Agent skill | `skills/relux-release/SKILL.md` | Adopt, tag, verify, troubleshoot |
| Owner guide | `docs/OWNER-GUIDE.md` | Org secrets, tap token, APT/AUR keys, first release |

## Tools

| Tool | Used for | Command | Output |
| --- | --- | --- | --- |
| sh / bash | Gate scripts and tests | `make test` (runs `tests/run-all.sh`) | stdout; scratch under `.temp/test-env.*`, kept only on failure |
| shellcheck | Static script checks | `make check` | stdout |
| actionlint | Workflow checks (`.github/actionlint.yaml` ignores one known-new context) | `make check` | stdout |
| Go | Build and vet the lint | `make check`; run: `go run ./cmd/release-standard-lint [-main PKG] [-binary NAME] [-workflow FILE] .goreleaser.yml` | stdout, exit 0/1/2 |
| git, ssh-keygen | Test fixtures | used by `tests/tag-gate-test.sh` | scratch only |
| GoReleaser v2.17.0 | Real-engine controls | `REQUIRE_ENGINE=1 GORELEASER=/path/to/goreleaser bash tests/engine-test.sh` (CI job `engine`; SKIP without a binary) | scratch only |
| cosign, gh | Release verification | `scripts/verify-release.sh --dir DIR --repo OWNER/NAME --tag vX.Y.Z [--attest]` | stdout |

## Using it

Adopting, tagging and verifying: `skills/relux-release/SKILL.md`.
Secrets and org setup: `docs/OWNER-GUIDE.md`. Design background: the release
standard design v1 (STORY-261113-i1yc0p).
