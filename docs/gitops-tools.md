# Vesper, Argo CD and Flux

Argo CD and Flux, the two GitOps tools of the CNCF, can already create an
environment for each pull request. People who run one of them ask why Vesper
exists. This page answers it: what each tool does for pull requests, what
Vesper does differently, what it does not do, and when to choose which.

In short: Argo CD and Flux deploy what your manifests describe, for each pull
request; Vesper gives each preview an isolated, bounded and expiring
environment by default, without a GitOps setup. They can work together.

## Argo CD

An [ApplicationSet with the Pull Request generator](https://argo-cd.readthedocs.io/en/stable/operator-manual/applicationset/Generators-Pull-Request/)
asks the API of the forge (GitHub, GitLab, Gitea, Bitbucket, Azure DevOps)
for the open pull requests of a repository, and generates an Argo CD
Application for each, from a template. The template receives the number of
the pull request, its branch and its head commit, among others.

- It polls the forge every 30 minutes by default (`requeueAfterSeconds`), or
  reacts to the webhooks of the forge, sent to the ApplicationSet controller.
- Filters select the pull requests: by labels, by branch, by title.
- When a pull request no longer matches the filters, its Application is
  removed; with the resources finalizer, which Argo CD adds by default,
  [deleting the Application deletes what it deployed](https://argo-cd.readthedocs.io/en/stable/operator-manual/applicationset/Application-Deletion/).
- The Application deploys anything Argo CD can: manifests, Helm charts,
  kustomize, from any repository, and keeps them in sync with Git.

## Flux

The [Flux Operator](https://fluxoperator.dev/docs/resourcesets/github-pull-requests/),
an extension of Flux, adds two resources. A `ResourceSetInputProvider` of type
`GitHubPullRequest` lists the open pull requests of a repository, and a
`ResourceSet` renders a set of resources for each, typically a `GitRepository`
and a `HelmRelease`, from the number, the branch and the head commit of the
pull request.

- It polls GitHub on an interval, or reacts to a webhook through a Flux
  `Receiver`.
- Labels select the pull requests (`filter.labels`), and can pause one
  (`skip.labels`). Previews are often opt-in: a team adds a label to the pull
  requests that need one.
- The ResourceSet [deletes the resources it no longer renders](https://fluxoperator.dev/docs/crd/resourceset/):
  when a pull request is merged, closed or loses its label, its environment
  goes.
- Like Argo CD, it deploys anything Flux can, and keeps it in sync with Git.

## What Vesper does differently

Argo CD and Flux deploy what the template describes. Isolation, limits and
cleanup are what you write into it. Vesper starts from the other end: a
preview is disposable, and its safety does not depend on each template.

- **Isolated by default.** Each preview gets a namespace of its own with the
  restricted Pod Security profile, a ResourceQuota, a LimitRange and a
  default-deny NetworkPolicy. The policy also blocks the API server and the
  kubelets through the addresses of the nodes, which is easy to miss in a
  hand-written policy ([security model](security.md)). It is verified with
  kindnet, Calico and Cilium.
- **Gone on time.** A TTL, from 1 minute to 7 days, deletes a preview and its
  namespace even if its pull request stays open for weeks. Neither the Pull
  Request generator of Argo CD nor the ResourceSet of Flux has a time to live
  of its own.
- **No GitOps setup to start.** `vesperctl create` runs a preview from an
  image; there is no repository of manifests to prepare.
- **Built for previews.** `vesperctl get` shows the phase, the URL, the
  expiry, the conditions and the events of a preview; the operator exposes
  metrics per phase.

## What Vesper does not do

- **Pull request events, today.** Argo CD and Flux react to pull requests
  now; Vesper does from v0.2 ([milestone](https://github.com/eventide-oss/vesper/milestone/6)).
- **Any manifest.** A preview runs one image behind a Service. Argo CD and
  Flux deploy a whole application, with its Helm chart, its databases and its
  cloud resources. Vesper plans previews of several components for v0.3
  ([milestone](https://github.com/eventide-oss/vesper/milestone/7)).
- **Continuous sync.** Argo CD and Flux keep the cluster in line with Git and
  detect drift. Vesper restores the objects of a preview that someone changes,
  but there is no Git behind them.

## What teams report

The first feedback on Vesper came from teams that run previews with these
tools. As experience, not as a benchmark:

- A team on the Argo CD Pull Request generator gets the isolation of its
  production manifests, a separate database through the generator, and
  cleanup when the pull request closes. What it misses is a TTL for pull
  requests that stay open; charging teams for their resources makes them
  close them.
- Another team dropped Argo CD for ephemeral environments, because of the
  sync and of the management of many repositories at scale, and keeps it for
  production and staging.
- A team on Flux uses the ResourceSet with the GitHub pull request provider,
  with previews opt-in by label, and is happy with it.

## When to choose which

- **You already run Argo CD or Flux, and your application is a set of
  manifests or a Helm chart:** their pull request generators are probably the
  right tool. Write the isolation and the limits into the template, and plan
  for pull requests that stay open.
- **You want previews that are isolated and expire without writing it into
  each template, or you do not run GitOps:** Vesper.
- **Both:** let Argo CD or Flux install and upgrade Vesper itself, from the
  `install.yaml` of a release and a [kustomize overlay](quickstart.md), and
  let Vesper run the previews.

## At a glance

| | Argo CD | Flux | Vesper v0.1 |
| --- | --- | --- | --- |
| Environment per pull request | yes, ApplicationSet | yes, Flux Operator ResourceSet | from v0.2 |
| Opt-in by label | yes | yes | from v0.2 |
| Cleanup when the pull request closes | yes | yes | from v0.2 |
| Time to live | no | no | yes, 1 minute to 7 days |
| Isolation by default | what the template says | what the template says | namespace, Pod Security, quota, NetworkPolicy |
| What it runs | any manifests, Helm, kustomize | any manifests, Helm | one image per preview; several from v0.3 |
| Needs a GitOps repository | yes | yes | no |
| Continuous sync from Git | yes | yes | no |

The facts on Argo CD and Flux come from their documentation, linked above, in
November 2026. If one is out of date, open an issue.
