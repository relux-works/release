---
name: relux-release
description: Relux Works release standard for Go binaries. Use when adopting the shared go-release.yml in a repository, cutting a signed release tag, verifying a published release (cosign, checksums, provenance), or diagnosing a failed release run (Apple notary 401, partial signing secrets, tag-gate refusals). Triggers - release, cut a release, tag a release, goreleaser, notarize, notary 401, release-signers, verify release, homebrew tap, релиз, выпустить релиз.
---

# Relux release standard

One shared repository holds the release workflow, gates, template and lint.
Every command below uses this variable; it is the only place this skill names
the repository (placeholder, see the standard's NOTES.md):

```bash
STANDARD_REPO=relux-works/release
STANDARD_CHECKOUT="$PWD/.temp/release-standard"   # a local clone of $STANDARD_REPO
[ -d "$STANDARD_CHECKOUT" ] || gh repo clone "$STANDARD_REPO" "$STANDARD_CHECKOUT"
```

Parts you will touch:

| Part | Path in the standard |
| --- | --- |
| Reusable workflow | `.github/workflows/go-release.yml` |
| Caller workflow template | `template/release.yml` |
| GoReleaser template | `template/.goreleaser.yml` |
| Tag gate / signing gate | `scripts/tag-gate.sh`, `scripts/signing-gate.sh` |
| Checksum signing wrapper | `scripts/cosign-sign-checksum.sh` |
| Standard checkout check | `scripts/standard-checkout-check.sh` |
| Consumer verification | `scripts/verify-release.sh` |
| Config lint | `cmd/release-standard-lint` |

Hard rules for agents:

- Never read, print, copy or set secret values. Secrets are org-level and
  managed by the owner (docs/OWNER-GUIDE.md). Name checks only.
- Release tags are SSH-signed annotated tags made with the host's agent signing
  setup. Never sign with another key and never push a tag the owner did not ask for.
- Never move or delete a published tag. A bad release gets a new patch version.
- Never change an org secret's repository list yourself. When a repository
  needs a secret, ask the owner, and point out that `gh secret set --repos`
  replaces the whole selected-repository list (docs/OWNER-GUIDE.md, section 2):
  the additive `PUT orgs/<org>/actions/secrets/<name>/repositories/<id>`
  endpoint, or the full current list, keeps existing consumers. Names-only
  check: `gh api orgs/relux-works/actions/secrets/ASC_KEY_P8/repositories --jq '.repositories[].full_name'`.

## 1. Adopt the standard in a repository

Preconditions: pure Go with `CGO_ENABLED=0` (cgo/Swift binaries wait for
`darwin-sign.yml`, phase P3); the repository is in the selected-repository
list of the org secrets (ask the owner).

1. Copy the templates from a checkout of the standard at the commit you will pin:

   ```bash
   git -C "$STANDARD_CHECKOUT" rev-parse HEAD     # the SHA to pin
   cp "$STANDARD_CHECKOUT/template/.goreleaser.yml" .goreleaser.yml
   mkdir -p .github/workflows
   cp "$STANDARD_CHECKOUT/template/release.yml" .github/workflows/release.yml
   ```

2. Replace every `__PLACEHOLDER__`:
   - `.goreleaser.yml`: `__BINARY__`, `__MAIN_PACKAGE__`, `__VERSION_SYMBOL__`
     (the `-X` target, e.g. `main.buildVersion`), `__HOMEPAGE__`,
     `__MAINTAINER__`, `__DESCRIPTION__`, `__LICENSE__`, `__REPOSITORY_NAME__`.
   - `release.yml`: `__STANDARD_SHA__` (full 40-hex SHA, never a branch or tag),
     `__STANDARD_VERSION__` (comment only), `__MAIN_PACKAGE__`, `__BINARY__`,
     `__VERSION_SOURCE_FILE__`, `__VERSION_SOURCE_REGEX__`.
   - The version regex is an extended regex matching exactly one whole line,
     anchored with `^...$`, with the version in capture group 1. Example for
     `var buildVersion = "0.7.0"`: `^var buildVersion = "([^"]*)"$`.
3. Add `.github/release-signers` (SSH allowed_signers format, namespace git):

   ```text
   ivan@relux.works namespaces="git" ssh-ed25519 AAAA...
   ```

   Only public keys. The gate reads this file from the tag commit, and the
   tag commit must be on main, so changes to it go through review on main.
4. Make sure `LICENSE`, `NOTICE` and `README.md` exist (the archives include them).
5. Lint before opening the PR:

   ```bash
   (cd "$STANDARD_CHECKOUT" && go run ./cmd/release-standard-lint \
     -main ./cmd/<binary> -binary <binary> \
     -workflow "$OLDPWD/.github/workflows/release.yml" "$OLDPWD/.goreleaser.yml")
   ```

   Exit 0 prints `release-standard-lint: ok`; exit 1 lists named findings.
   Keep the template's signing parts as they are: the signing gate stays the
   only `before` hook, the `signs` entry runs the shared wrapper, notarize
   `ids` select every Darwin build with exactly the build ids (no padding),
   and no `env` list sets `RELEASE_REQUIRE_SIGNING`, `RELEASE_STANDARD_SCRIPTS`,
   a signing name, `TAP_GITHUB_TOKEN`, `PATH` or a `GO*` name other than
   `GOFLAGS` in build env (`protected-env`). Env entries and flags are
   checked byte for byte. Nothing in the config may run your own commands
   during the release (`caller-hook`): no other before hooks, build hooks,
   extra `signs`, `binary_signs`, `docker_signs`, `publishers`, `upx`, SBOM
   commands or `-toolexec`. Put repo-specific checks in `admission_command`.
6. Optional inputs in `release.yml` (defaults in brackets):
   `allowed_signers_path` [.github/release-signers], `require_signed_tag` [true],
   `require_on_main` [true], `allow_prerelease` [false], `require_signing` [true],
   `go_version_file` [go.mod], `admission_command` [none], `submodules` [false],
   `runner` [macos-15]. Use `admission_command` for repo-specific checks
   (for example `go test ./cmd/x -run '^TestReleaseVersionPinned$'`).
7. Remove the repo's old inline release workflow and any CI script that greps
   it (curator's `release-workflow-gate.sh` is replaced by the lint).

The cosign identity changes on adoption: signatures now come from the shared
workflow, so update any verification docs or footers to section 3's form.

## 2. Cut a signed release tag

1. Land the version bump on main (the version source must equal the tag).
2. Sync and check:

   ```bash
   git fetch origin && git switch main && git merge --ff-only origin/main
   test "$(git rev-parse HEAD)" = "$(git rev-parse origin/main)"
   ```

3. Tag with the host's agent signing setup (on ivmbp):

   ```bash
   source ~/Developer/ReluxWorks/ivmbp-host-context/bin/agent-git-env
   VERSION=0.7.1
   git tag -s "v$VERSION" -m "v$VERSION"
   git cat-file tag "v$VERSION" | grep -q '^-----BEGIN SSH SIGNATURE-----$'
   git -c gpg.ssh.allowedSignersFile=.github/release-signers verify-tag "v$VERSION"
   ```

   The signing key must be listed in `.github/release-signers`. Prereleases use
   `vX.Y.Z-rc.N` and need `allow_prerelease: true`; the version source must
   then read `X.Y.Z-rc.N` exactly.
4. Push only that tag: `git push origin "v$VERSION"`.
5. Watch: `gh run list --workflow release.yml --limit 3`, then `gh run watch <id>`.

## 3. Verify a published release

```bash
mkdir -p .temp/verify && cd .temp/verify
gh release download "v$VERSION" --repo relux-works/<repo> \
  --pattern 'checksums.txt*' --pattern '*darwin_arm64.tar.gz'
"$STANDARD_CHECKOUT/scripts/verify-release.sh" --dir . \
  --repo relux-works/<repo> --tag "v$VERSION" --attest
```

It authenticates `checksums.txt` with cosign against the shared workflow's
identity for this caller repo and tag, checks every downloaded file with
`sha256sum -c` (or `shasum -a 256 -c`), refuses files not listed in the
checksums (`--allow-unlisted` to only warn), and with `--attest` runs
`gh attestation verify --signer-workflow` on archives, packages and the
checksums. Add `--standard-ref <sha>` to require the exact standard commit.

Manual equivalent of the cosign step:

```bash
cosign verify-blob \
  --certificate checksums.txt.pem --signature checksums.txt.sig \
  --certificate-identity-regexp "^https://github\.com/$STANDARD_REPO/\.github/workflows/go-release\.yml@" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-github-workflow-repository relux-works/<repo> \
  --certificate-github-workflow-ref "refs/tags/v$VERSION" \
  checksums.txt
```

## 4. Troubleshooting

### Apple notary 401 Unauthorized

Seen on curator rc.8: the build signs, then `notarytool` submission fails with 401.
It means App Store Connect rejected the API key triple, not that the code is wrong.

1. Names-only check of where the three ASC secrets come from:

   ```bash
   gh secret list --repo relux-works/<repo>        # repo-level copies shadow org ones
   gh api repos/relux-works/<repo>/actions/organization-secrets --jq '.secrets[].name'
   ```

   A repo-level `ASC_*` copy overrides the org secret. A stale repo copy is the
   usual cause after a key rotation.
2. Hand the rest to the owner (agents never see values). Likely causes, in order:
   `ASC_KEY_ID` does not belong to `ASC_KEY_P8`; `ASC_ISSUER_ID` is from another
   team; the key was revoked in App Store Connect (Users and Access, Integrations);
   `ASC_KEY_P8` lost its BEGIN/END lines or newlines when pasted.
3. After the owner fixes the secrets, rerun the failed run: `gh run rerun <id> --failed`.
   Notarization fails before anything is published, so a rerun of the same tag is safe.
   Do not retag.

### Partial signing configuration refused

`signing-gate: REFUSED[partial_signing_config]: N of 5 ...; missing: <NAMES>`
(exit 3) means some of MACOS_SIGN_P12, MACOS_SIGN_P12_PASSWORD, ASC_ISSUER_ID,
ASC_KEY_ID, ASC_KEY_P8 reached the job and some did not. Usual causes: the repo
is missing from one org secret's selected-repository list (often after a
`gh secret set --repos` call replaced that list); the caller does not pass
`secrets: inherit`; a repo-level copy is set to an empty value. Ask the owner
to fix the scoping. Never work around it by unsetting secrets.

The gate runs as the publish job's own `Signing presence gate` step before
GoReleaser, and again as GoReleaser's first `before` hook.

`signing-gate: REFUSED[signing_required_but_absent]` (exit 4): none of the five
arrived and `require_signing` is true. Same fixes. Only the owner may decide to
ship unsigned by setting `require_signing: false`.

`signing-gate: REFUSED[invalid_require_signing]` (exit 2): `RELEASE_REQUIRE_SIGNING`
is not `true` or `false` (local snapshots: set it to `false`).

### Tag-gate refusals

Every refusal prints `tag-gate: REFUSED[<reason>]: <detail>`.

| Exit | Reason | Meaning | Fix |
| ---: | --- | --- | --- |
| 2 | usage | Bad workflow inputs | Check the `with:` block of release.yml |
| 10 | bad_tag_name | Not `vX.Y.Z` / `vX.Y.Z-rc.N` | Use a valid tag name |
| 11 | prerelease_not_allowed | rc tag without `allow_prerelease` | Opt in, or tag a stable version |
| 12 | not_a_tag_ref | Run was not triggered by that tag | Trigger only on tag push |
| 13 | tag_missing | Tag not in the checkout | Push the tag; check fetch-depth |
| 14 | lightweight_tag | Not an annotated tag | Delete the unpublished tag, `git tag -s` |
| 15 | tag_name_mismatch | Tag object names another tag | Recreate the tag under its own name |
| 16 | signers_file_missing | No allowed_signers at the tag commit | Add `.github/release-signers` on main |
| 17 | unsigned_tag | No signature | Re-tag with `git tag -s` and agent-git-env |
| 18 | non_ssh_signature | OpenPGP/X.509 signature block, also with `require_signed_tag: false` | Sign with the SSH key |
| 19 | untrusted_signature | Key not in allowed_signers, or tampered | Use a listed key; owner adds keys on main |
| 20 | commit_mismatch | Tag commit is not the checked-out commit | The tag moved; never move tags |
| 21 | version_source_unreadable | Regex matches 0 or 2+ lines (empty captures count), no group, or bad version | Fix `version_source_regex` / file |
| 22 | version_mismatch | Tag differs from the version source | Bump the source on main, new tag |
| 23 | main_ref_missing | origin/main not fetched | Check the checkout step |
| 24 | not_on_main | Tag commit is not on main | Merge to main first, tag the merged commit |
| 25 | unsafe_path | Absolute or `..` path input | Use a repo-relative path |

A refused tag never published anything. If the tag was wrong and nothing was
released, the owner decides whether to delete and recreate it; otherwise bump
the patch version.

### Other failures

- `standard_sha_unavailable` / `standard_sha_mismatch`: the runner did not
  provide `job.workflow_sha` or the checkout drifted. Do not replace it with a
  branch ref; report it.
- `standard-checkout-check: REFUSED[standard_tree_modified]`: a file of the
  standard checkout changed before GoReleaser, or (in the step after
  GoReleaser) while it ran. Report it; do not rerun around it.
- `cosign-sign-checksum: REFUSED[certificate_missing|signature_missing|cosign_failed]`:
  cosign did not produce `checksums.txt.pem` and `.sig`; nothing was published.
  Check the job's `id-token: write` permission and the cosign log.
- Lint findings in admission: fix the caller config; each finding ID maps to
  one rule in `cmd/release-standard-lint/lint.go`.
- Tap upload failure: `TAP_GITHUB_TOKEN` is missing, expired or lacks
  contents:write on homebrew-tap. Owner rotates it (docs/OWNER-GUIDE.md).
