# Previews from pull requests

This guide connects Vesper to GitHub: each pull request of a repository gets
a preview of its own, which follows its pushes and closes with it, and a
comment on the pull request gives its URL. It is the GitHub integration of
v0.2 ([ADR-0007](adr/0007-github-integration.md)), available from
`v0.2.0-rc.2`.

It has two parts, for two roles:

- **[Part A](#part-a-connect-the-cluster-to-github)**, once per cluster, by
  its administrator: create a GitHub App and install `vesper-github`.
- **[Part B](#part-b-add-a-repository)**, once per repository: build the
  image of each pull request, and allow the repository.

Values to replace:

| Placeholder | Meaning |
| --- | --- |
| `acme` | the GitHub organization, or user, that owns the App and the repositories |
| `acme/app` | a repository that gets previews |
| `hooks.preview.example.com` | the public host of the webhook endpoint |
| `123456` | the ID of your GitHub App |

All the commands run on your machine, with `kubectl` connected to the
cluster.

## How it works

```text
pull request ──► GitHub Actions builds and pushes ghcr.io/acme/app:pr-42-<commit>
     │                         │
     │ webhook                 │ webhook: the run succeeded
     ▼                         ▼
           vesper-github (in the cluster)
     reads the pull request, finds the PreviewSource of acme/app
                               │
                               ▼
     PreviewEnvironment acme-app-pr-42 ──► the operator runs the preview
                               │
                               ▼
     a deployment and a comment on the pull request, with the URL
```

- **Vesper never builds code.** The CI of the repository builds the image of
  each commit; Vesper waits for that workflow to succeed, then deploys the
  image.
- **A GitHub App** is the identity of Vesper on GitHub: it receives the
  events of the pull requests, and writes the comment. Each Vesper cluster
  has an App of its own; there is no shared App.
- **A `PreviewSource`** allows a repository, and says where its images are.
  A repository where the App is installed but that has no source gets no
  preview.

## Before you start

- Vesper `v0.2.0-rc.2` or later on a cluster reachable from the Internet,
  with a preview domain and HTTPS. [Try Vesper on a server](try-on-a-server.md)
  sets this up. GitHub must reach the webhook endpoint over HTTPS: a kind
  cluster on a laptop cannot receive the webhooks.
- A host for the webhook endpoint. Under the preview domain, such as
  `hooks.preview.example.com`, the wildcard DNS record and the wildcard
  certificate of the previews already cover it.
- Owner rights on the GitHub organization, or a personal account, that will
  own the App.

## Part A: connect the cluster to GitHub

### A1. Make the webhook secret

GitHub signs each webhook with a secret shared with Vesper, and Vesper
rejects any delivery whose signature does not match. Make one, in a file only
you can read, without printing it:

```sh
( umask 077 && openssl rand -hex 32 > webhook-secret )
```

### A2. Create the GitHub App

Open **Settings → Developer settings → GitHub Apps → New GitHub App** of the
organization, at `https://github.com/organizations/acme/settings/apps/new`
(for a personal account: `https://github.com/settings/apps/new`), and fill in
the form:

| Field | Value |
| --- | --- |
| GitHub App name | a name of your own, unique on GitHub, such as `Acme Preview`. The bot that comments is `<slug>[bot]` |
| Homepage URL | your site, or `https://eventide-oss.github.io/vesper/` |
| Callback URL, Setup URL | empty |
| Request user authorization (OAuth) during installation | unchecked |
| Webhook → Active | checked |
| Webhook URL | `https://hooks.preview.example.com/webhook` |
| Webhook secret | the content of `webhook-secret`, see below |
| SSL verification | Enable |

Paste the secret without printing it: on macOS, `pbcopy < webhook-secret`,
paste into the field, then empty the clipboard with `pbcopy < /dev/null`.
**Check that the browser did not fill the field itself**: it looks like a
password field, and a saved password there makes every delivery fail with a
401.

**Repository permissions**, and no other permission:

| Permission | Access | Why |
| --- | --- | --- |
| Actions | Read-only | to know when the image of a commit is built |
| Deployments | Read and write | the deployment of each commit, and its "View deployment" button |
| Metadata | Read-only | imposed by GitHub |
| Pull requests | Read and write | to read pull requests, and write the comment |

**Subscribe to events:** **Pull request** and **Workflow run**, nothing else.

**Where can this GitHub App be installed?** **Only on this account**. See
[Repositories of another account](#repositories-of-another-account) before
you choose **Any account**.

Create the App. GitHub sends a first `ping` to the webhook URL at once; it
fails, since `vesper-github` is not installed yet. You send it again in A6.

On the page of the App, note the **App ID**, a number: it is not a secret.
Under **Display information**, you can upload a logo, such as
[the one of Vesper](images/logo-github-app.png).

### A3. Generate the private key

`vesper-github` signs its requests to GitHub with the private key of the App.
On the page of the App, generate one: **Private keys → Generate a private
key**. Depending on the version of the GitHub interface, the section is also
called **Key pairs**, under **Credentials**. Use that one: a **Client
secret** is for a sign-in flow that Vesper does not use.

The browser downloads a file such as `acme-preview.2026-11-13.private-key.pem`.

### A4. Store the App in the cluster

`vesper-github` reads three files from the Secret `vesper-github` of the
namespace `vesper-system`:

```sh
kubectl -n vesper-system create secret generic vesper-github \
  --from-literal=app-id=123456 \
  --from-file=private-key=$HOME/Downloads/acme-preview.2026-11-13.private-key.pem \
  --from-file=webhook-secret=webhook-secret

# The sizes only, never the values.
kubectl -n vesper-system describe secret vesper-github
```

You should see `app-id`, `private-key` (about 1700 bytes) and
`webhook-secret` (65 bytes). Then delete the local copies: the cluster keeps
them.

```sh
rm ~/Downloads/acme-preview.2026-11-13.private-key.pem webhook-secret
```

A lost key is not a problem: generate a new one, update the Secret, and
delete the old key on the page of the App. To change the webhook secret, make
a new one as in A1, set it with **Change secret** on the page of the App,
then update the Secret: `vesper-github` reads it at each delivery.

### A5. Install vesper-github

`install-github.yaml`, published with each release, holds `vesper-github`
and its rights, with the image of the release pinned by digest. Set the host
of the webhook with a kustomize overlay, so that an upgrade keeps it. Save
this as `vesper-github/kustomization.yaml`, with your host:

```yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
- https://github.com/eventide-oss/vesper/releases/download/v0.2.0-rc.2/install-github.yaml
patches:
- target:
    kind: Ingress
    name: vesper-github-webhook
  patch: |-
    - op: replace
      path: /spec/rules/0/host
      value: hooks.preview.example.com
    - op: replace
      path: /spec/tls/0/hosts/0
      value: hooks.preview.example.com
```

```sh
kubectl apply -k vesper-github
kubectl -n vesper-system rollout status deploy/vesper-github
kubectl -n vesper-system logs deploy/vesper-github | head -3
```

The log starts with `Starting Vesper v0.2.0-rc.2`. To upgrade later, change
the version in the URL and apply the overlay again. Keep the operator in an
overlay the same way, so that an upgrade keeps its `--preview-domain` and
`--preview-tls`.

### A6. Check the webhook

On the page of the App, **Advanced → Recent Deliveries** lists what GitHub
sent. Open the failed `ping` and click **Redeliver**: the response must be
**200**. A 401 means that the secret of the App and the one of the cluster
differ: set it again with **Change secret** (A4).

From the command line, the endpoint answers 401 to an unsigned delivery:

```sh
curl -s -o /dev/null -w '%{http_code}\n' -X POST https://hooks.preview.example.com/webhook
```

## Part B: add a repository

### B1. Install the App on the repository

On the page of the App, **Install App**, then **Install** next to the
account. Choose **Only select repositories** and the repository: the App
then sees nothing else. GitHub shows the permissions it asks for: read access
to actions and metadata, read and write access to deployments and pull
requests.

### B2. Build the image of each pull request

The application must run as a non-root user, and listen on a port above
1024: previews run under the Pod Security profile `restricted`
([security model](security.md)). For a static site, the image
`nginxinc/nginx-unprivileged` serves on port 8080 as user 101.

Copy [the example workflow](../examples/github-actions/preview.yml) to
`.github/workflows/preview.yml` in the repository, and set `IMAGE` to the
image of the application, in lowercase, such as `ghcr.io/acme/app`. It runs
on each pull request, builds the image and pushes it as
`ghcr.io/acme/app:pr-<number>-<head commit>`. It pins each action to a commit
and has the least rights: it reads the code and writes packages, nothing
more. It tags the image with the **head commit** of the pull request,
`github.event.pull_request.head.sha`, and not with `github.sha`, which is a
merge commit that GitHub makes for pull requests: Vesper would never find
that image.

### B3. Make the image public

Vesper pulls images without credentials, so the image must be public;
private images come with [#130](https://github.com/eventide-oss/vesper/issues/130).
GitHub creates the package at the first push, with the default visibility of
the organization, often private. After the first run of the workflow, open
the package, at
`https://github.com/orgs/acme/packages/container/app/settings`, then
**Danger Zone → Change visibility → Public**. A private image leaves the
preview in `ImagePullBackOff`; it starts on its own once the image is public.

### B4. Allow the repository

An administrator of the cluster creates the namespace where the previews of
the repository are listed, and the [`PreviewSource`](preview-sources.md) of
the repository:

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: previews-acme
---
apiVersion: vesper.eventide.dev/v1alpha1
kind: PreviewSource
metadata:
  name: acme-app # the previews are named acme-app-pr-<number>
spec:
  repository: acme/app
  image: ghcr.io/acme/app:pr-{{number}}-{{sha}}
  buildWorkflow: preview.yml
  namespace: previews-acme
  trigger: All
  forkPullRequests: Deny
  defaults:
    port: 8080
    ttl: 24h
```

```sh
kubectl apply -f source.yaml
kubectl get previewsources
```

`image` must match the tag the workflow pushes, and `buildWorkflow` its file
name. [Allow a repository](preview-sources.md) describes every field.

### B5. Open a pull request

Open a pull request on the repository. In about two minutes:

1. The workflow builds and pushes the image.
2. When it succeeds, `vesper-github` creates the preview, and comments
   *Your preview is starting*.
3. Once the preview runs, the comment says *Your preview is ready*, with the
   link, and **View deployment** appears on the pull request.

Follow it from the cluster:

```sh
kubectl -n vesper-system logs deploy/vesper-github -f
kubectl get previewenvironments -n previews-acme
```

## On the pull request

- **Each push** builds a new image. The preview keeps running the previous
  commit until the new image is built, then switches to it, and its TTL
  starts again, up to 7 days in all. Each commit has a deployment of its own.
- **Stop a preview:** add the label `no-preview` with `trigger: All`, or
  remove the label `preview` with `trigger: Label`. Do the opposite to start
  it again. Create these labels in the repository first.
- **The TTL ends:** the preview is deleted, and the comment says *Your
  preview expired*. Push a commit to bring it back.
- **The pull request closes** or merges: the preview is deleted, and the
  comment says *Your preview was stopped*.
- **Drafts** get a preview once they are ready for review. **Forks** get none
  by default ([Pull requests from forks](preview-sources.md#pull-requests-from-forks)).

## Repositories of another account

An App created with **Only on this account** can only be installed on the
account that owns it. For the repositories of another organization, either:

- **that organization runs its own Vesper**, and creates its own App: the
  usual case, since previews run the code of the repository on the cluster;
- or **you set the App to Any account** (**Advanced → Make public**), and
  that organization installs it. Its webhooks then reach your cluster, but no
  preview runs until you create a `PreviewSource` for its repository:
  allowing a repository stays your decision.

## Troubleshooting

| Symptom | Cause | Fix |
| --- | --- | --- |
| The deliveries of the App get 401 | the webhook secret of the App and of the Secret differ, often because the browser filled the field | set it again with **Change secret** (A6) |
| The deliveries time out or get 404 | the host, the DNS record or the ingress | check `kubectl -n vesper-system get ingress`, and that the URL ends with `/webhook` |
| `vesper-github` is not ready | the Secret is missing, or lacks a key | check the three keys with `kubectl describe secret` (A4) |
| No comment, and the log says `no PreviewSource` | the repository is not allowed | create its source (B4) |
| No comment, and the log asks `does the namespace ... exist?` | the namespace of the source is missing | create it (B4) |
| No comment, and the workflow is green | the image tag does not match, or the workflow is not the `buildWorkflow` | compare the tag with `image` of the source, and use `head.sha` (B2) |
| The preview stays starting, its pod is in `ImagePullBackOff` | the image is private | make it public (B3) |
| The pod is rejected | the image runs as root, or on a port below 1024 | use a non-root image (B2), see [Troubleshooting](troubleshooting.md) |
| The push of the workflow fails with `denied` | the job lacks `packages: write`, or the package belongs to another repository | keep the permissions of the example, and grant the repository access to the package in its settings |

The resync looks at every open pull request every 10 minutes, so a missed
webhook delays a preview, but never loses it. For the internals of
`vesper-github`, see [Development](development.md#vesper-github).
