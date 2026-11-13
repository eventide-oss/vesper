# Releasing Vesper

A release is cut by pushing a version tag. The release workflow
(`.github/workflows/release.yml`) then publishes the operator image, builds
the other assets, writes the notes and creates a **draft** release; a
maintainer reads it and publishes it. The image is in the registry as soon as
the workflow pushes it; the release, its notes and `install.yaml` are public
only after that click.

## What a release contains

- `vesperctl` for Linux, macOS and Windows on `amd64` and `arm64`, as
  `.tar.gz` (`.zip` for Windows) archives with `LICENSE` and `README.md`,
  built by GoReleaser from `.goreleaser.yaml` (see
  [Release builds](development.md#release-builds)).
- The operator image `ghcr.io/eventide-oss/vesper:<tag>`, for `linux/amd64`
  and `linux/arm64`, built from the `Dockerfile`: distroless, running as the
  non-root user 65532, with the version in its first log line and in its
  `org.opencontainers.image.*` labels. There is no `latest` tag.
- `install.yaml`: the CRD, the RBAC, the namespace `vesper-system`, the
  operator Deployment and the metrics Service, rendered from
  `config/default`, with the image pinned by tag **and digest**. What it
  installs is the image the workflow built, even if the tag moved.
- `install-github.yaml`: `vesper-github`, the GitHub integration, from the
  same image ([Development](development.md#vesper-github)); optional.
- `checksums.txt`, the SHA-256 of every archive and of the two bundles.
- A provenance attestation for every file of `checksums.txt` and for the
  image (stored in the registry, next to it), signed with the identity of the
  workflow run, which records the repository, the tag and the commit it was
  built from.
- Notes generated from the titles of the commits since the previous tag, in
  groups: breaking changes, features, bug fixes, other changes. They are the
  squash-merge titles of the pull requests, which is why those follow
  Conventional Commits.

## Install a release

```sh
kubectl apply -f https://github.com/eventide-oss/vesper/releases/download/v0.1.0-rc.1/install.yaml
kubectl -n vesper-system rollout status deploy/vesper-controller-manager
kubectl -n vesper-system logs deploy/vesper-controller-manager | head -1   # Starting Vesper {"version": "v0.1.0-rc.1"}
```

The operator then runs with its default flags: no preview domain, so
previews get no public address. Setting the flags is described in
[Development](development.md#public-address) for now, and will be in the
quickstart (#51).

## Versions

Versions follow [Semantic Versioning](https://semver.org/): `vMAJOR.MINOR.PATCH`.
Before `v1.0.0`, a minor version may break the API (`v1alpha1`). A tag with a
suffix, such as `v0.1.0-rc.1`, makes a pre-release: GitHub does not show it
as the latest release. Use a release candidate to try the assets before the
final tag.

## Cut a release

1. Check that `main` is green on CI and holds everything the release needs.
   Run the **Isolation** workflow on `main` (Actions → Isolation → Run
   workflow) and wait for it to pass: it checks the network isolation with
   Calico and Cilium.

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

4. Follow the run in the **Actions** tab, workflow **Release**: the `image`
   job, then the `release` job. It takes about ten minutes.

5. Open the draft in **Releases**: check the notes (edit them if needed: add
   a summary at the top, fix a wording), the list of assets (six archives,
   `install.yaml`, `checksums.txt`), and that a release candidate is marked
   **Pre-release**.

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
workflow and the commit. `install.yaml` is checked the same way.

The image, by the digest `install.yaml` names:

```sh
image=$(grep -o 'ghcr.io/eventide-oss/vesper:[^ ]*' install.yaml)
gh attestation verify "oci://$image" --repo eventide-oss/vesper
docker buildx imagetools inspect "$image"   # linux/amd64 and linux/arm64
```

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
- **The image was pushed but the release job failed**: fix and tag again as
  above. The new run pushes the image again under the same tag; the old
  image stays in the registry, untagged, and can be deleted from the
  package page.
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

**The image is public.** The first release creates the package
`ghcr.io/eventide-oss/vesper`. If the organization creates packages as
private, open the package (the **Packages** section of the repository or of
the organization), then **Package settings → Change visibility → Public**.
Without it, clusters cannot pull the image without credentials.

**Published releases cannot change.** On the main Settings page (**General**),
scroll down to the **Releases** section and check **Enable release
immutability**. It applies to releases published after it is enabled.

## Permissions of the workflow

The workflow is read-only by default. Each job gets only what it needs:

| Job | Permission | Why |
| --- | --- | --- |
| `image` | `packages: write` | push the image to GHCR |
| `release` | `contents: write` | create the draft release and upload its assets |
| both | `id-token: write` | get the OIDC identity of the run, which signs the attestations |
| both | `attestations: write` | store the attestations |

It uses no other secret than the token GitHub gives to each run, and its
actions are pinned to a full commit SHA.
