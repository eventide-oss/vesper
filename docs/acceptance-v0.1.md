# Acceptance of v0.1

This page checks v0.1 against the criteria of the framing letter of the
project (November 2026): its objectives, its use cases, the scope of the MVP,
its rules of good practice, and the gate of the release. Each criterion is
marked:

- ✅ met, with the proof;
- ⚠️ met differently, with the reason;
- ⏳ not met yet, with the issue that follows it.

**Release checked:** `v0.1.0-rc.3` (commit `4192480`), the release that
v0.1.0 is cut from. **Date:** 2026-11-10.

## How to run it again

```sh
hack/acceptance.sh v0.1.0-rc.3
```

The script creates a fresh kind cluster (Kubernetes 1.37), installs the
release from its `install.yaml`, downloads and checks `vesperctl`, then runs
each check below and prints what it sees. It uses a kubeconfig of its own and
deletes the cluster at the end. It needs kind, kubectl, curl, jq, Docker and
the GitHub CLI. The public address over HTTPS needs a real server, and follows
[Try Vesper on a server](try-on-a-server.md).

## Objectives

| Objective | Indicator | Result |
| --- | --- | --- |
| Create and destroy an environment from a `PreviewEnvironment` | full cycle shown on a local kind cluster | ✅ the run creates, serves, updates, extends, expires and deletes previews |
| No orphan environment | 0 Vesper namespace left after the TTL, checked by an end-to-end test | ✅ the e2e test `TestCLIExpiry` checks that the namespace is gone; the run ends with 0 namespaces labelled `app.kubernetes.io/managed-by=vesper` |
| An idiomatic, tested Go controller | unit and envtest tests green in CI on every pull request | ✅ the `test` job runs both with `-race` on every pull request; `main` is green |
| Understand each concept before using it | a learning journal, and an ADR per structural choice | ⚠️ six ADRs ([docs/adr](adr/)); the journal has its template only: ⏳ to write |
| A project others can join | contribution guide, 10 `good first issue`, a first external contributor | ✅ [CONTRIBUTING.md](../CONTRIBUTING.md), 10 open good first issues; ⏳ the first external contributions, #150 and #152, are in review |
| Prepare for the CNCF Sandbox | Apache 2.0 license and a neutral GitHub organization | ✅ [LICENSE](../LICENSE), SPDX headers checked in CI, organization `eventide-oss` |

## Use cases

| Use case | Result |
| --- | --- |
| 1. Create an environment from a container image (by hand; from a pull request in v0.2) | ✅ `vesperctl create demo --image nginxinc/nginx-unprivileged:1.27-alpine` becomes Ready and serves its page |
| 2. Delete the environment when its TTL passes (when the pull request closes in v0.2) | ✅ a preview with a TTL of 1 minute is deleted with its namespace |
| 3. Extend or stop an environment early with `vesperctl` | ✅ `vesperctl extend demo --by 2h`; `vesperctl delete other --yes` deletes it and its namespace |
| 4. See the state, the events and the URL with `vesperctl list` and `vesperctl get` | ✅ `get` shows the phase, URL, expiry, conditions and, since #151, the events |
| 5. Enforce resource limits per environment | ✅ a ResourceQuota and a LimitRange in each preview namespace; a pod over the quota is refused |

## Scope of the MVP

| Item | Result |
| --- | --- |
| CRD `PreviewEnvironment` (`v1alpha1`): image, port, TTL, resource limits | ⚠️ image, port, replicas, env and TTL are fields; resource limits are set by the administrator with operator flags (`--quota-*`, `--default-*`), not per preview, so that a user cannot raise their own budget. Limits per team are planned with the policies of v0.4. |
| Controller: a namespace per environment, with a Deployment, a Service, HTTP access and a ResourceQuota | ✅ plus a LimitRange and a NetworkPolicy; HTTP access through an Ingress with `--preview-domain` |
| Guaranteed deletion: finalizer, owner references, TTL expiry | ⚠️ finalizer and TTL ✅; the objects of a preview are tracked by labels instead of owner references, because an owner reference cannot point to another namespace ([ADR-0002](adr/0002-namespace-per-preview.md)); deleting the preview namespace deletes them |
| Readable status: conditions and Kubernetes events | ✅ conditions `Ready`, `Available`, `Expired` with reasons, and events at each change |
| CLI `vesperctl`: `create`, `list`, `get`, `extend`, `delete` | ✅ all five, with table, JSON and YAML output and documented exit codes (1 for a missing preview, 2 for a usage error) |
| Basic metrics exposed by the controller | ✅ `vesper_previews_active` per phase and `vesper_previews_expired_total`; 401 without a token |
| Unit tests, envtest and an end-to-end test on kind | ✅ the `test`, `integration` and `e2e` jobs of CI |

## Good practices

| Rule | Result |
| --- | --- |
| `gofmt`, `go vet` and `golangci-lint` in CI | ✅ the `vet` and `lint` jobs, with `gosec` |
| Errors wrapped with `%w`, `context.Context` first | ✅ followed in the code and checked in review |
| Table-driven tests run with `-race` | ✅ `make test` |
| Idempotent reconciliation | ✅ an object that matches is not written again ([architecture](architecture.md#a-reconciliation)) |
| Owner references on created objects | ⚠️ labels instead, as above |
| A finalizer for cleanup | ✅ `vesper.eventide.dev/cleanup` |
| Status in `status.conditions`, and events | ✅ |
| Versioned API, validated in the CRD schema | ✅ `v1alpha1`; the image must be pinned, the TTL is bounded to 1m–168h, replicas to 1–5 |
| Minimal RBAC, never cluster-admin | ✅ [operator permissions](development.md#operator-permissions); no access to Secrets |
| A namespace per environment, with quotas and the `restricted` Pod Security level, and the weak isolation documented | ✅ a privileged pod is refused; [security model](security.md) |
| Three test levels | ✅ |
| SemVer, Conventional Commits, changelog, GoReleaser releases | ✅ [Releasing](release.md) |
| CONTRIBUTING, CODE_OF_CONDUCT, SECURITY, GOVERNANCE, issue templates | ✅ |
| Apache 2.0 and DCO | ✅ the `dco` job checks every commit |
| An ADR per structural choice | ✅ six ADRs |
| Aim for the OpenSSF Best Practices badge, in time | ⏳ after v0.1, as the letter says |
| A learning journal | ⏳ to write, as above |

## Gate: v0.1 published

| Item | Result |
| --- | --- |
| README | ✅ status, quickstart, links to the guides |
| License | ✅ |
| Contribution guide | ✅ |

## The run, in detail

On a fresh kind cluster, with `v0.1.0-rc.3`:

| Check | Seen |
| --- | --- |
| Install | `kubectl apply -f install.yaml` rolls the operator out; it logs `Starting Vesper {"version": "v0.1.0-rc.3"}`; its image is pinned by digest |
| Release artifacts | the `vesperctl` archive passes its checksum and its attestation; the image passes its attestation, for `linux/amd64` and `linux/arm64`, user `65532:65532` |
| Create | Ready; the namespace holds the Deployment, Service, ResourceQuota, LimitRange and NetworkPolicy; the page answers through a port-forward |
| Validation | `latest`, a TTL of 30s and 6 replicas are refused by the API server, exit code 2, with a message naming the field |
| Pod Security | a privileged pod is refused: `violates PodSecurity "restricted:latest"` |
| Quota | a pod asking for 5Gi is refused: `exceeded quota: preview` |
| Network | from a preview: another preview blocked, the API server blocked, the internet reached |
| Update | a new image rolls out, the preview is Ready again |
| Extend | `extended by 2h0m0s: ttl 3h0m0s` |
| Expiry | a 1-minute preview and its namespace are gone |
| Delete | the preview and its namespace are gone |
| Status | `vesperctl get` shows the conditions and the events (`NamespaceCreated`, `Provisioning`, `DeploymentAvailable`) |
| CLI | `list` and `get` in table, JSON and YAML; exit 1 for a missing preview, 2 for an unknown output format |
| Metrics | 401 without a token; with one, every phase and `vesper_previews_expired_total 1` after the expiry |
| Cleanup | 0 Vesper namespaces left |

On a real server ([#96](https://github.com/eventide-oss/vesper/issues/96),
with v0.1.0-rc.2, which differs from rc.3 by the events of `vesperctl get`
and docs): k3s on a VPS, a preview opened from a browser and a phone at its
public address, over HTTPS with a Let's Encrypt wildcard certificate, and no
Secret in the preview namespace. That run found an isolation gap, fixed in
rc.2 ([GHSA-m437-fj8p-6x8h](https://github.com/eventide-oss/vesper/security/advisories/GHSA-m437-fj8p-6x8h)).

## Follow-ups

| Issue | What |
| --- | --- |
| #150, #152 | the first external contributions, in review |
| #155 | the message of `Provisioning` during a rollout reads `1/1 replicas available`, seen in this run |
| #154 | `TestList` can fail when it runs across a second |
| — | the learning journal (`docs/journal`) |
| — | the OpenSSF Best Practices badge, and checking the name before any CNCF application |
| [P5](https://github.com/eventide-oss/vesper/milestone/6) | previews from pull requests (GitHub App), private images, protected URLs |

**Verdict:** v0.1 meets the MVP of the framing letter. The two ⚠️ items are
choices recorded above; the ⏳ items do not block the release.
