# Relux release standard

Shared release machinery for Relux Works Go binaries: one reusable GitHub
Actions workflow, the admission gates, a GoReleaser template, a config lint,
an agent skill and an owner guide. Callers keep only their per-binary
`.goreleaser.yml` and a short `release.yml`; this repository owns the tool
versions, the gates and the publishing steps.

The repository name is not decided (`relux-works/release` or
`relux-works/.github`). Every file names it once; NOTES.md lists each place.

## Status

P0 scratch, not published. Nothing here has run in GitHub Actions yet, and the
tests have not been executed (only static checks; see NOTES.md). Two action
pins are still `PIN-REQUIRED`, so `go-release.yml` fails closed until they
are filled in. `linux-repos.yml` is disabled and unimplemented by design.

## Parts

| Part | Path | What it does |
| --- | --- | --- |
| Go release workflow | `.github/workflows/go-release.yml` | Reusable `workflow_call`: admission (tag gate, lint, optional caller command) then publish (GoReleaser v2.17.0, cosign, syft, notarization, provenance) |
| Linux repositories | `.github/workflows/linux-repos.yml` | Reusable, **disabled**: APT index and AUR `-bin` contract with failing stubs |
| Tag gate | `scripts/tag-gate.sh` | Annotated, SSH-signed, allowlisted tag equal to the ref, HEAD and the version source; optional on-main and prerelease rules; named refusals with distinct exit codes |
| Signing gate | `scripts/signing-gate.sh` | All-or-none presence of the five darwin signing secrets, with `require_signing` |
| Toolchain | `scripts/go-toolchain.sh` | GoReleaser `tool`: GOTOOLCHAIN from the go.mod `go` line |
| Consumer verification | `scripts/verify-release.sh` | cosign identity of the shared workflow, `sha256sum -c`, optional `gh attestation verify` |
| Lint | `cmd/release-standard-lint` | Checks a caller `.goreleaser.yml` (and optionally its workflow) against the template |
| Templates | `template/.goreleaser.yml`, `template/release.yml` | What a caller copies |
| Tests | `tests/` | Gate, signing, lint and harness-control tests (throwaway git and ssh-keygen fixtures) |
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
| cosign, gh | Release verification | `scripts/verify-release.sh --dir DIR --repo OWNER/NAME --tag vX.Y.Z [--attest]` | stdout |

## Using it

Adopting, tagging and verifying: `skills/relux-release/SKILL.md`.
Secrets and org setup: `docs/OWNER-GUIDE.md`. Design background: the release
standard design v1 (STORY-261113-i1yc0p).
