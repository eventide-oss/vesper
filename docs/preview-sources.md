# Allow a repository

A `PreviewSource` allows a GitHub repository to have previews of its pull
requests, and says how to build them. It is the list of the GitHub
integration: a pull request of a repository without a `PreviewSource` gets no
preview, even if the GitHub App of Vesper is installed on it
([ADR-0007](adr/0007-github-integration.md)).

> `vesper-github`, the GitHub integration of v0.2, reads the sources.
> [Previews from pull requests](github-integration.md) sets it up, from the
> GitHub App to the first preview.

## Who creates it

An administrator of the cluster. A `PreviewSource` is **cluster-scoped**: it
belongs to no namespace, and creating one needs a right on the whole cluster,
not on a namespace. A team that can create previews in its namespace cannot
allow a repository by itself, since that lets the repository run code on the
cluster.

The install bundle has two ClusterRoles for it:
`vesper-previewsource-admin-role`, for the administrators of Vesper, and
`vesper-previewsource-viewer-role`, to read them. There is no editor role for
teams, on purpose.

## An example

```yaml
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
    replicas: 1
    ttl: 24h
    env:
      - name: APP_ENV
        value: preview
```

```sh
kubectl apply -f config/samples/vesper_v1alpha1_previewsource.yaml
kubectl get previewsources
```

## The fields

| Field | Required | Meaning |
| --- | --- | --- |
| `metadata.name` | yes | at most 40 lowercase letters, digits and hyphens: the previews of the source are named `<name>-pr-<number>` |
| `repository` | yes | the repository, `<owner>/<name>`; GitHub compares it without case |
| `image` | yes | the image of a pull request, as a template: `{{number}}` is the number of the pull request, `{{sha}}` its head commit |
| `buildWorkflow` | no | the file name of the GitHub Actions workflow that builds the image, such as `preview.yml` |
| `namespace` | yes | where the `PreviewEnvironment` objects of the source are created; each preview still runs in a namespace of its own |
| `trigger` | no | `All` (default) or `Label`, see below |
| `forkPullRequests` | no | `Deny` (default) or `RequireLabel`, see below |
| `defaults` | no | the port, replicas, TTL and environment of the previews, with the bounds of a `PreviewEnvironment` |

Every field is described in the [CRD reference](crd-reference.md).

### The image

The CI of the repository builds an image for each commit of a pull request
and pushes it; Vesper never builds code. The template says where to find it.
It must:

- contain `{{sha}}`, so that each commit has an image of its own, and Vesper
  knows which one runs;
- give a valid image reference in lowercase, with a tag that is not `latest`,
  once `{{number}}` and `{{sha}}` are replaced. They are the only
  placeholders.

With `buildWorkflow`, a preview switches to the image of a commit once a run
of that workflow succeeds on the commit, so that it never runs an image that
is not built yet. Without it, Vesper uses the image as soon as the pull
request event arrives: use it only when the image is pushed before.

### Which pull requests get a preview

- `trigger: All`: every pull request. Add the label `no-preview` to a pull
  request to stop its preview, remove it to start it again.
- `trigger: Label`: only the pull requests with the label `preview`. Remove
  the label to stop the preview.

Drafts get a preview once they are ready for review.

### Pull requests from forks

Anyone can open a pull request from a fork of a public repository, and its
code is not reviewed.

- `forkPullRequests: Deny`, the default: they get no preview.
- `forkPullRequests: RequireLabel`: they get one once someone with write
  access to the repository adds the label `preview`, whatever the trigger.

**The risk.** The preview runs the code of a stranger on your cluster, with
the network access and the resources the isolation of Vesper gives a
preview ([security model](security.md)). Adding the label means *I read this
code and I accept to run it*. Read it first, the workflow that builds it too.

How the label approves a commit:

- Only the label of someone with the permission write, maintain or admin
  counts. Vesper asks GitHub who added it last; triage can add labels, but
  approves nothing. A label added by someone else deletes the preview.
- The label approves the commits pushed **before** it, never the later ones:
  otherwise the author could push new code once a maintainer approved the
  old one. Vesper compares the time of the label with the creation of the
  run of `buildWorkflow` on the commit, which GitHub sets, unlike the date of
  a commit. When a new commit comes after the label, the preview stays on
  the approved commit, and the comment says so: remove the label and add it
  again to run the new one.
- Forks need `buildWorkflow`: without it, nothing tells when a commit was
  pushed, and no fork gets a preview.
- Do not make `buildWorkflow` run on the `labeled` event: its new run would
  look like a commit pushed after the label, and nothing would be approved.
  Reopening a pull request starts a new run too: add the label again then.

**The image.** On a pull request from a fork, GitHub gives the workflow a
read-only token, and no secrets: the workflow of the [GitHub
guide](github-integration.md) cannot push the image of a fork. Until Vesper
documents a safe way to build them
([#189](https://github.com/eventide-oss/vesper/issues/189)), use
`RequireLabel` only if your CI already pushes images of forks.

### The defaults

`defaults` sets the previews of the source, with the bounds of a
`PreviewEnvironment`: port 1 to 65535 (8080 by default), 1 to 5 replicas
(1), a TTL from 1m to 168h (24h), and at most 64 environment variables. Each
new commit resets the TTL of a preview, up to 7 days in all.

The environment variables are readable by anyone who can read the source: do
not put secrets in them.

## One source per repository

Kubernetes cannot refuse two sources for the same repository, since the rule
would span two objects. The integration refuses both, and logs why, rather
than pick one: delete one of them. A repository without a source gets no
preview either, and the log of `vesper-github` says so.

The namespace of a source must exist: `vesper-github` has no right to create
namespaces, and logs the error until an administrator creates it.
