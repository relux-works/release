# Owner guide: Relux release standard

For Ivan. Everything here involves secret values or org settings, so agents do
not do it. This guide lists names only; no value appears in any document.

Commands below use one variable for the shared repository, `relux-works/release`:

```bash
STANDARD_REPO=relux-works/release
ORG=relux-works
```

## 1. Create the shared repository

1. Pick the name and create it public (callers in other repos must be able to
   check it out and reuse its workflow): github.com, **+** (top right),
   **New repository**, Owner `relux-works`, name, **Public**, no template.
2. Allow reuse of its workflows: repository **Settings**, **Actions**,
   **General**, **Access**: public repositories are reusable by default; if the
   org restricts actions, add `$STANDARD_REPO` under org **Settings**,
   **Actions**, **General**, **Policies**, "Allow specified actions and
   reusable workflows".
3. Protect `main` (Settings, Rules, Rulesets): require a pull request and
   signed commits. Callers pin a commit SHA, so a change here reaches a
   caller only when the caller bumps its pin.

## 2. Org-level Actions secrets

Names (create each once at org level):

| Secret | What it is | Status today |
| --- | --- | --- |
| `MACOS_SIGN_P12` | Developer ID Application certificate + key, .p12, base64 | repo-level on launcher, curator |
| `MACOS_SIGN_P12_PASSWORD` | Password of that .p12 | repo-level on launcher, curator |
| `ASC_ISSUER_ID` | App Store Connect API issuer ID | repo-level on launcher, curator |
| `ASC_KEY_ID` | App Store Connect API key ID | repo-level on launcher, curator |
| `ASC_KEY_P8` | App Store Connect API private key (.p8 contents) | already org-level |
| `TAP_GITHUB_TOKEN` | Fine-grained token for homebrew-tap (section 3) | repo-level on curator |

Selected repositories for every secret: `curator-agent-launcher`, `curator`,
`a2a-swarm`, `swarma-identity`, and the Namecoin release repository when it
exists. Add others only when they adopt the standard.

Steps for each new secret:

1. Open `https://github.com/organizations/relux-works/settings/secrets/actions`
   (org **Settings**, **Secrets and variables**, **Actions**, tab **Secrets**).
2. **New organization secret**. Name from the table. Paste the value.
3. **Repository access**: choose **Selected repositories**, click the gear,
   tick the repositories above, **Update selection**.
4. **Add secret**.

For the existing `ASC_KEY_P8`: click its name in the same list, under
**Repository access** click the gear, add the missing repositories, **Update
selection**, **Save changes**. In the UI the repositories already ticked stay
ticked; only add ticks.

> **Warning: `--repos` replaces the whole list.** `ASC_KEY_P8` already serves
> other repositories. `gh secret set ASC_KEY_P8 --org relux-works --visibility
> selected --repos a,b` (and the same call when updating any org secret's
> value) sets the selected repositories to exactly `a,b`: every repository not
> in that list silently loses the secret, and its next notarized release fails.
> Either pass the full current list plus the new names, or add repositories
> one at a time with the additive endpoint, which never removes anything.
>
> Names only, no values (run before and after, and compare):
>
> ```bash
> gh api "orgs/$ORG/actions/secrets/ASC_KEY_P8/repositories" --paginate \
>   --jq '.repositories[].full_name' | sort > asc-key-p8-repos.before
> ```
>
> Additive, one repository at a time (the owner runs it; no value is involved):
>
> ```bash
> REPO_ID=$(gh api "repos/$ORG/swarma-identity" --jq .id)
> gh api -X PUT "orgs/$ORG/actions/secrets/ASC_KEY_P8/repositories/$REPO_ID"
> ```
>
> If you do use `gh secret set ... --repos`, build the list from the
> `before` file plus the new names, never from memory, and check afterwards
> that the new list contains every line of the `before` file:
>
> ```bash
> gh api "orgs/$ORG/actions/secrets/ASC_KEY_P8/repositories" --paginate \
>   --jq '.repositories[].full_name' | sort > asc-key-p8-repos.after
> comm -23 asc-key-p8-repos.before asc-key-p8-repos.after   # must print nothing
> ```
>
> The same applies to every secret in the table once it serves more than one
> repository. Setting a value stays an owner-only step in the UI or an
> interactive prompt; never pass a value on the command line. Before that, make sure the org value is the
current key (the curator ASC secrets were re-set on 2026-10-26 after the
notary 401; if the org copy is older, update its value too with **Update
secret**).

Values must match exactly what works today on the launcher (v0.7.0 notarized).
Keep the .p12 and .p8 in 1Password; never paste them into chat or a terminal
that an agent can read.

## 3. TAP_GITHUB_TOKEN

A fine-grained personal access token that can write only to homebrew-tap.

1. If needed, allow fine-grained tokens for the org: org **Settings**,
   **Personal access tokens**, **Settings**, allow fine-grained tokens
   (approval required is fine; approve the request in step 4).
2. Your avatar, **Settings**, **Developer settings**, **Personal access tokens**,
   **Fine-grained tokens**, **Generate new token**.
3. Fill in:
   - Token name: `relux-release homebrew-tap writer`;
   - Resource owner: `relux-works`;
   - Expiration: 90 days (pick a date, write it in your calendar);
   - Repository access: **Only select repositories**, `relux-works/homebrew-tap`;
   - Permissions, Repository permissions: **Contents: Read and write**
     (Metadata: Read-only is added automatically). Nothing else.
4. **Generate token**, approve it under org **Settings**, **Personal access
   tokens**, **Pending requests** if approval is on, then save it as the org
   secret `TAP_GITHUB_TOKEN` (section 2).

Rotation: about a week before expiry, generate a new token with the same
settings, **Update secret** on `TAP_GITHUB_TOKEN`, then delete the old token
under **Fine-grained tokens**. An expired token only breaks the cask upload of a
stable release; archives and packages are already published by then. Later,
a GitHub App with contents:write on homebrew-tap would remove the expiry chore.

Known gap: tap commits are unsigned bot commits to homebrew-tap `main`, which
has no protection. Adding signed-commit rules there would break publishing.

## 4. Remove the repo-level copies (names-only check first)

Repo-level secrets override org-level secrets with the same name, so the org
value is used only after the repo copy is gone. For each of
`curator-agent-launcher` and `curator`:

1. Names only, no values:

   ```bash
   REPO=curator-agent-launcher   # then curator
   gh secret list --repo "$ORG/$REPO"
   gh api "repos/$ORG/$REPO/actions/organization-secrets" --jq '.secrets[].name' | sort
   ```

   The second list must contain all six names from section 2 (all five
   signing names for the launcher; all six for curator).
2. Delete the repo-level copies, one name at a time:

   ```bash
   for name in MACOS_SIGN_P12 MACOS_SIGN_P12_PASSWORD ASC_ISSUER_ID ASC_KEY_ID ASC_KEY_P8 TAP_GITHUB_TOKEN; do
     gh secret delete "$name" --repo "$ORG/$REPO" || true
   done
   gh secret list --repo "$ORG/$REPO"     # expect none of the six
   ```

   Or in the UI: repository **Settings**, **Secrets and variables**,
   **Actions**, trash icon next to each name.
3. The next release of that repository (or a release candidate) proves the
   org values work. If notarization then fails with 401, the org value is stale:
   update it (section 2) and rerun the failed run.

## 5. Later: APT and AUR keys (phase P5)

`linux-repos.yml` stays disabled until these exist and `enable: true` is set.

### APT signing key

Generate on an offline machine (or at least outside any agent session):

```bash
export GNUPGHOME="$(mktemp -d)"
gpg --quick-generate-key 'Relux Works APT <apt@relux.works>' rsa4096 sign 2y   # prompts for a passphrase
FPR=$(gpg --list-keys --with-colons | awk -F: '/^fpr:/ { print $10; exit }')
gpg --armor --export "$FPR" > relux-works-apt.asc                 # public, publish this
gpg --armor --export-secret-keys "$FPR" > apt-signing-secret.asc  # upload, then destroy
gpg --fingerprint "$FPR"
```

RSA 4096 works with every apt version still in use; Ed25519 needs newer apt.
Set a passphrase in the pinentry prompt; never pass it on the command line.

Upload as org secrets, scoped to the release repositories:
`APT_GPG_PRIVATE_KEY` (the whole armored `apt-signing-secret.asc`) and
`APT_GPG_PASSPHRASE`. Keep an offline backup of the secret key and a revocation
certificate (`gpg --gen-revoke "$FPR"`), then `rm -P apt-signing-secret.asc`
and remove the temporary `GNUPGHOME`.

Publish the public key and fingerprint:

1. Commit `relux-works-apt.asc` to `relux-works/apt` at the site root.
2. Put the full fingerprint in that repository's README and on relux.works,
   with the install snippet using `signed-by=/etc/apt/keyrings/relux-works-apt.asc`.
3. A push token for `relux-works/apt` is also needed (`APT_REPO_TOKEN`,
   fine-grained, Contents: Read and write on that repository only). This is
   still an open design item.

### AUR SSH key

1. Create the AUR account that will maintain the `-bin` packages
   (aur.archlinux.org, **Register**).
2. Generate a dedicated key:

   ```bash
   ssh-keygen -t ed25519 -C 'relux-works AUR publisher' -f aur_ed25519 -N ''
   ssh-keygen -lf aur_ed25519.pub     # fingerprint to publish
   ```

3. AUR **My Account**, **SSH Public Key**: paste `aur_ed25519.pub`, save.
4. Org secret `AUR_SSH_PRIVATE_KEY`: the contents of `aur_ed25519`. Then
   delete the local private file (keep an offline backup if you want).
5. Publish the fingerprint next to the APT one.

Then allow creation of `relux-works/apt` (GitHub Pages), and tell the agents to
implement the stubs in `linux-repos.yml`.

## 6. Cut the first release (launcher, phase P1)

1. An agent opens the adoption PR in `curator-agent-launcher`: templates
   filled in, `release.yml` calling `$STANDARD_REPO` at a pinned SHA, lint green.
   Review and merge it.
2. Dry run first (design P1): tag a release candidate on a fork, or on the
   repository with `allow_prerelease: true` and the version source at
   `X.Y.Z-rc.1`. A prerelease does not move the Homebrew cask.
3. Tag (the agent can do this on request with agent-git-env, or you):

   ```bash
   git switch main && git pull --ff-only
   git tag -s v0.7.1 -m v0.7.1
   git push origin v0.7.1
   ```

4. Watch the run: Admission, then Publish.
5. Verify with `scripts/verify-release.sh` (skill section 3) and check that the
   cask appeared in `relux-works/homebrew-tap/Casks/`.

## 7. Checklist

- [ ] Shared repository name chosen and repository created public
- [ ] `main` protected; reusable workflows allowed for the org
- [ ] Org secrets `MACOS_SIGN_P12`, `MACOS_SIGN_P12_PASSWORD`, `ASC_ISSUER_ID`, `ASC_KEY_ID` created
- [ ] `ASC_KEY_P8` value current and its repository list extended additively (before/after names-only comparison prints nothing)
- [ ] `TAP_GITHUB_TOKEN` fine-grained token created (homebrew-tap, Contents write), expiry in calendar
- [ ] All six secrets scoped to: launcher, curator, a2a-swarm, swarma-identity (Namecoin repo later)
- [ ] Names-only check passes; repo-level copies deleted on launcher and curator
- [ ] Launcher adoption PR merged; dry run on an rc passed
- [ ] First release tagged and verified
- [ ] Later: `relux-works/apt` created, APT GPG key and AUR key uploaded, fingerprints published
