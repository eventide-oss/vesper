# Releasing Vesper

A release is cut by pushing a version tag. The release workflow
(`.github/workflows/release.yml`) then builds the assets, writes the notes and
creates a **draft** release; a maintainer reads it and publishes it. Nothing
is public before that click.

## What a release contains

- `vesperctl` for Linux, macOS and Windows on `amd64` and `arm64`, as
  `.tar.gz` (`.zip` for Windows) archives with `LICENSE` and `README.md`,
  built by GoReleaser from `.goreleaser.yaml` (see
  [Release builds](development.md#release-builds)).
- `checksums.txt`, the SHA-256 of every archive.
- A provenance attestation for every archive, signed with the identity of the
  workflow run, which records the repository, the tag and the commit it was
  built from.
- Notes generated from the titles of the commits since the previous tag, in
  groups: breaking changes, features, bug fixes, other changes. They are the
  squash-merge titles of the pull requests, which is why those follow
  Conventional Commits.

The operator image and `install.yaml` join the release with #56.

## Versions

Versions follow [Semantic Versioning](https://semver.org/): `vMAJOR.MINOR.PATCH`.
Before `v1.0.0`, a minor version may break the API (`v1alpha1`). A tag with a
suffix, such as `v0.1.0-rc.1`, makes a pre-release: GitHub does not show it
as the latest release. Use a release candidate to try the assets before the
final tag.

## Cut a release

1. Check that `main` is green on CI and holds everything the release needs.

2. Tag the commit of `main`, with a signed, annotated tag:

   ```sh
   git switch main && git pull
   git tag -s v0.1.0-rc.1 -m "Vesper v0.1.0-rc.1"
   git tag -v v0.1.0-rc.1        # check the signature
   ```

   `-s` signs the tag with your Git signing key (GPG or SSH, as set in
   `gpg.format` and `user.signingkey`). The workflow refuses a tag that
   points to a commit outside `main`.

3. Push the tag, and only the tag:

   ```sh
   git push origin v0.1.0-rc.1
   ```

4. Follow the run in the **Actions** tab, workflow **Release**. It takes a
   few minutes.

5. Open the draft in **Releases**: check the notes (edit them if needed: add
   a summary at the top, fix a wording), the list of assets, and that a
   release candidate is marked **Pre-release**.

6. Click **Publish release**.

## Verify a release

Anyone can check a downloaded archive:

```sh
gh release download v0.1.0-rc.1 --repo eventide-oss/vesper \
  --pattern 'vesperctl_*_linux_amd64.tar.gz' --pattern checksums.txt
shasum -a 256 --check --ignore-missing checksums.txt   # sha256sum on Linux
gh attestation verify vesperctl_0.1.0-rc.1_linux_amd64.tar.gz --repo eventide-oss/vesper
```

The checksum proves the file is the one listed in `checksums.txt`. The
attestation proves the archive was built by the release workflow of
`eventide-oss/vesper`, from the tag; `gh attestation verify` prints the
workflow and the commit.

## When it fails

- **The workflow fails before the draft exists**: fix the cause on `main`
  through a pull request, then delete the tag and tag again:

  ```sh
  git push origin --delete v0.1.0-rc.1
  git tag -d v0.1.0-rc.1
  ```

  Then go back to step 2, on the new commit.
- **The draft is wrong**: delete the draft in **Releases**, then delete and
  push the tag again as above. Running the workflow again for the same tag
  replaces the draft.
- **A published release is wrong**: do not change it. Publish a new patch
  version (or the next release candidate) with the fix. Published assets
  must never change: people and tools may have checked their checksums.

## Repository settings

These are set once, by an admin, from the **Settings** tab of the repository.

**Only maintainers can create, move or delete a version tag.** In the left
sidebar, under "Code, planning, and automation", open **Rules**, then
**Rulesets**. Click **New ruleset**, then **New tag ruleset**:

- Ruleset name: `release tags`; enforcement status: **Active**.
- Target tags: **Add a target**, **Include by pattern**, `v*`.
- Tag protections: **Restrict creations**, **Restrict updates** and
  **Restrict deletions**.
- Bypass list: **Add bypass**, then the **Repository admin** role (and
  **Maintain**, if maintainers have that role).
- Click **Create**.

**Published releases cannot change.** On the main Settings page (**General**),
scroll down to the **Releases** section and check **Enable release
immutability**. It applies to releases published after it is enabled.

## Permissions of the workflow

The workflow is read-only by default. Its only job gets:

| Permission | Why |
| --- | --- |
| `contents: write` | create the draft release and upload its assets |
| `id-token: write` | get the OIDC identity of the run, which signs the attestation |
| `attestations: write` | store the attestation |

It uses no other secret than the token GitHub gives to each run, and its
actions are pinned to a full commit SHA.
