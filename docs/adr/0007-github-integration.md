# ADR-0007: Integrate with GitHub through a GitHub App and a separate webhook component

- **Status:** Accepted
- **Date:** 2026-11-12
- **Deciders:** @damnthonyy
- **Issue / PR:** #125

## Context

Today a preview is created by hand, with `vesperctl` or `kubectl`, by someone
who can reach the API server of the cluster. v0.2 must give a pull request its
preview by itself: when it opens, when a commit is pushed, and until it
closes.

The constraints:

- **Vesper never builds code.** The CI of each project builds the image of a
  pull request and pushes it to a registry; Vesper runs it (ADR-0002).
- **The code may live elsewhere**: in another repository or organization than
  the cluster, and it may be private.
- **The API server of the cluster stays closed to the Internet**: the server
  guide drops everything but 22, 80 and 443.
- **The operator has narrow rights** and no access to Secrets (security
  model). Whatever faces the Internet must not widen them.
- **A pull request is untrusted input**: its branch, its title and its code
  come from its author, who may be a stranger when it comes from a fork.

The launch of v0.1.0 brought feedback from teams running previews with Argo
CD and Flux ([comparison](../gitops-tools.md)): a team dropped Argo CD for
ephemeral environments because of the cost of syncing many repositories;
another misses a time to live for pull requests that stay open; a third makes
previews opt-in with a label. Each shapes a decision below.

## Decision

### A GitHub App, installed by the owner of each repository

Vesper talks to GitHub as a **GitHub App**. The owner of a repository installs
it, on chosen repositories; the App receives their events and gets an
**installation token**, valid one hour, limited to those repositories and to
these permissions:

| Permission | Access | Why |
| --- | --- | --- |
| Metadata | read | required by GitHub for every App |
| Pull requests | write | read the pull requests and their labels; write the comment of the preview |
| Deployments | write | show "View deployment" on the pull request, with its state |
| Actions | read | receive `workflow_run`, to know when the image of a commit is built |
| Packages | read | only for private images, as ADR-0008 decides (#130) |

It subscribes to two events: `pull_request` and `workflow_run`.

### A separate component, `vesper-github`, in front of the operator

The integration is **a component of its own**, `vesper-github`: a second
program of the same repository and release, shipped in the same image and run
as its own Deployment in `vesper-system`. It is the only part of Vesper that
faces the Internet.

```
GitHub ──webhook, signed──> Ingress (hooks.<domain>) ──> vesper-github ──PreviewEnvironment──> operator
                                                              │
GitHub <──comment, Deployment status, installation tokens─────┘
```

It does five things:

1. **Receive webhooks** over HTTPS, through the ingress controller, on a host
   of its own. It checks the signature `X-Hub-Signature-256` (HMAC SHA-256
   with the webhook secret, compared in constant time) before it parses the
   payload; it rejects a missing or wrong signature with 401, a payload over
   5 MiB, and any event it does not handle.
2. **Get installation tokens** from the private key of the App.
3. **Translate** a pull request into a `PreviewEnvironment`, following the
   `PreviewSource` of its repository: create, update, delete.
4. **Report** the phase of each preview back to the pull request.
5. **Resync** every 10 minutes: list the open pull requests of each allowed
   repository and fix what a lost webhook missed.

Its rights are the smallest that do this: create, update and delete
`PreviewEnvironment` objects, read `PreviewSource` objects, and read one
Secret, `vesper-github`, by name, which holds the App ID, the private key and
the webhook secret. It creates no namespace and touches no Deployment: the
operator does that, with every check of a preview (validation, quota,
isolation, TTL), and stays unaware of GitHub.

### `PreviewSource`: which repositories, and how

A webhook from a repository creates nothing until an administrator of the
cluster allows it with a **`PreviewSource`** (#126), cluster-scoped:

- `repository`: `<owner>/<name>`;
- `image`: the image of a pull request, as a template with `{{number}}` and
  `{{sha}}`, for example `ghcr.io/acme/app:pr-{{number}}-{{sha}}`;
- `buildWorkflow`: the file name of the workflow that builds the image, for
  example `preview.yml`; empty if the image is pushed before the pull request
  event arrives;
- the defaults of its previews: port, replicas, TTL, env;
- `trigger`: `All` (the default) or `Label`;
- `forkPullRequests`: `Deny` (the default) or `RequireLabel`;
- `namespace`: where its `PreviewEnvironment` objects live.

The name of a preview comes from the `PreviewSource`, which the administrator
wrote, and the number of the pull request: `<source>-pr-<number>`, cut and
hashed to a valid name. A branch name or a title never goes into a name, a
label, an image or a command.

### Which pull requests get a preview

By default, **every pull request** of an allowed repository does. Labels let
people choose:

- with `trigger: All`, the label **`no-preview`** stops the preview of a pull
  request, and removing it starts it again;
- with `trigger: Label`, only pull requests with the label **`preview`** get
  one, and removing the label stops it.

The comment of the preview says which label to add or remove, so that nobody
has to read the docs to stop one.

### From events to previews

| Event | Effect |
| --- | --- |
| `pull_request` `opened`, `reopened`, `ready_for_review` | create the preview if the image of the head commit is built |
| `pull_request` `synchronize` (new commit) | wait for its image, then update the preview to it |
| `workflow_run` `completed` with `success`, of the `buildWorkflow`, for the head commit of an open pull request | create or update the preview with that image |
| `pull_request` `labeled`, `unlabeled` | apply `preview` and `no-preview` as above |
| `pull_request` `closed` (merged or not) | delete the preview; its finalizer deletes its namespace |

Vesper never deploys an image that is not built yet: with a `buildWorkflow`,
it waits for its successful run on the head commit; a failed run is reported
on the pull request and changes nothing. Draft pull requests get a preview
only once they are ready for review.

### The time to live follows the activity

Each new commit **resets the TTL** of the preview: the integration extends it
so that it expires one TTL after the commit, as `vesperctl extend` does. An
active pull request keeps its preview; a forgotten one loses it, even if it
stays open for weeks, which Argo CD and Flux do not offer. The bound of
ADR-0004 stays: a preview lives at most 7 days; past that, the next commit
creates a new one.

An expired preview is not created again by the resync. GitHub keeps the record:
the Deployment of that commit is marked inactive, and the resync creates a
preview only for a head commit that has no Deployment yet. A new commit, or
the label `no-preview` removed and added again, brings it back.

### Reporting on the pull request

Two things, kept up to date from the phase of the preview:

- **A GitHub Deployment** per head commit, in an environment named after the
  preview: `in_progress` while Pending, `success` with the URL when Ready,
  `failure` with the reason when Failed, `inactive` when deleted or expired.
  GitHub shows "View deployment" on the pull request.
- **One comment**, written once and then edited, never a new one per change:
  the table of the preview (status, URL, image, expiry) and, in small print,
  how to stop it with the labels. It is the comment shown as a concept in the
  README.

### Forks

A pull request from a fork runs code from anyone. With `forkPullRequests:
Deny`, the default, it gets no preview. With `RequireLabel`, it gets one only
once someone with write access to the repository adds `preview`: the sender
of the `labeled` event is checked through the API, not trusted from the
payload. The image is the one the CI of the base repository built (#131).

### Threats and answers

| Threat | Answer |
| --- | --- |
| A forged webhook creates or deletes previews | HMAC signature checked in constant time before parsing; 401 otherwise; handling is idempotent, so a replayed delivery changes nothing |
| The public endpoint is flooded | size limit and timeouts; rejected requests are cheap; optional rate limit at the ingress controller; the endpoint has no access beyond previews |
| A stranger runs code through a fork | forks denied by default; with `RequireLabel`, the label must come from someone with write access |
| Any App installation runs code on the cluster | only repositories with a `PreviewSource` get previews; the rest is logged and ignored |
| Pull request data is crafted (branch, title) | never used in names, labels, images or commands; names come from the `PreviewSource` and the number |
| A token leaks | installation tokens last one hour and cover the installed repositories only; the private key and the webhook secret sit in one Secret, readable by `vesper-github` only; none is logged, nor the payloads |
| `vesper-github` is compromised | it can only create, change and delete previews, each still bound by the checks of the operator: no namespace, no Secret, no other workload |
| The URL of a private preview is public | protected URLs behind a GitHub login (#132) |

### Scale targets

Designed and tested for, at least: 50 repositories and 500 open pull requests
on one cluster; a preview created within 10 seconds of the event that makes
its image ready, before its pods start; less than 10% of the API quota of an
installation (5000 requests per hour), mostly the resync, which makes one
request per repository. Webhooks do the work; the resync is a safety net.
Each preview is reconciled on its own, without syncing a repository of
manifests.

## Alternatives considered

**The operator receives the webhooks itself.** One component less to run. But
the only part of Vesper on the Internet would hold every right of the
operator: namespaces, Deployments, NetworkPolicies, and later Secrets for
private images. A flaw in the parsing of a payload would reach all of them.
The operator would also need to know GitHub, and later GitLab.

**A personal access token or an OAuth App.** Simpler to set up for one person.
But a personal token acts as its owner, on every repository they can reach,
and lives until someone revokes it; it cannot be installed by another
organization. An App has its own identity, permissions chosen per App,
tokens of one hour, and installations per repository.

**Polling the GitHub API instead of webhooks**, as the pull request generators
of Argo CD and Flux do by default. No public endpoint. But a preview waits for
the next poll, and many repositories polled often spend the API quota; the
cost grows with the number of repositories, which is what a team reported
with Argo CD. Vesper keeps polling as a resync every 10 minutes only.

**The CI of each project calls Vesper**, with `vesperctl` and a kubeconfig, or
a token for an API of Vesper. No App to install. But every repository would
hold credentials of the cluster, its API server or a new API would face the
Internet, and fork pull requests would need those credentials too, or no
preview. The App keeps credentials out of the projects.

**The image is deployed as soon as the pull request event arrives**, and the
kubelet retries until it exists. Simpler, no `workflow_run`. But a preview
would sit in `ImagePullBackOff`, or run the image of the previous commit,
while CI builds, and the pull request would show a failure that is not one.

**A relay service outside the cluster** (a hosted webhook proxy). The cluster
would need no public endpoint. But Vesper would depend on a third party that
sees every event of every repository, which an open source operator should
not require.

## Consequences

- Vesper gains a second program, `vesper-github`, with its Deployment,
  Service, Ingress, RBAC and metrics in the install bundle, and a new
  cluster-scoped resource, `PreviewSource`. An installation without the
  integration runs as today.
- An administrator creates a GitHub App, stores its key and webhook secret in
  a Secret, and exposes a webhook host through the ingress controller: a
  guide documents it (#133), with the exact permissions and events.
- The webhook endpoint is the first part of Vesper that parses data from the
  Internet. It gets tests with forged and signed payloads, the security
  review of section 9, and metrics of received, rejected and handled
  deliveries.
- The time to live of a preview from a pull request follows its commits; one
  created by hand keeps the meaning of ADR-0004.
- GitHub holds the history of each preview (its Deployments): the cluster
  keeps no state of its own about pull requests.
- The client of the GitHub API and the signing of the App token are chosen in
  #127 and #128, with the rules for dependencies of the security review.
- The issues of the milestone follow this ADR: #126 (`PreviewSource` with
  `buildWorkflow` and `trigger`), #127 (`vesper-github` and its endpoint),
  #128 (events, labels, TTL), #129 (Deployment and comment), #131 (forks).
  Private images (#130) and protected URLs (#132) keep their own decisions.
- Revisit if GitLab comes (the translator would get a second source, the
  operator would not change), if previews need several images (ADR of #162),
  or if the scale targets are not met.
