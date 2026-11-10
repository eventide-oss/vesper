# Security model

Vesper runs code from pull requests, written by anyone allowed to open one,
inside a cluster you care about. This page says what Vesper protects against,
what it does not, and how to install it safely. To report a vulnerability, see
[SECURITY.md](../SECURITY.md).

**A namespace is a soft boundary.** Vesper puts several layers between a
preview and the rest of the cluster, but they all rest on the Linux kernel and
on Kubernetes: a vulnerability there can break them. Vesper does not provide
hard multi-tenant isolation. Do not run previews of untrusted code on a cluster
that also runs sensitive workloads.

## Threat model

| Who | What they control | What Vesper must prevent |
| --- | --- | --- |
| The code of a pull request | everything inside the containers of its preview | reaching other previews, the API server, the nodes, the services of the cluster; running as root or with privileges; using more than its share of the cluster; staying after its TTL |
| A user who creates previews | the spec of a `PreviewEnvironment`: image, port, replicas, env, TTL | running unpinned images, unbounded previews, or objects outside the preview namespace; taking over a namespace or an object Vesper did not create |
| Someone on the Internet | requests to the public address of previews | reaching anything but the application of a preview through the ingress controller |
| The operator, if compromised | its own rights | as little as possible: no access to Secrets, no pod creation, no write outside what previews need |

## Guarantees

Each guarantee names the code that enforces it and the test that checks it.

### A preview runs without privileges

The preview namespace enforces the Pod Security Standard `restricted`
(label `pod-security.kubernetes.io/enforce: restricted`), and the Deployment of
the preview meets it: non-root, no privilege escalation, every capability
dropped, the `RuntimeDefault` seccomp profile, no ServiceAccount token
mounted. The API server rejects any pod of the namespace that breaks the
standard, even one created by hand.

- Code: [`deployment.go`](../internal/controller/deployment.go) (`mutateDeployment`),
  [`previewenvironment_controller.go`](../internal/controller/previewenvironment_controller.go) (namespace labels).
- Tests: `TestReconcileCreatesDeployment`, `TestReconcileRestoresNamespaceLabels`,
  `TestEnvtestRejectsPrivilegedPod`.

### A preview has bounded resources

Each preview namespace gets a LimitRange, which gives every container a
default request and limit, and a ResourceQuota: by default 4 CPU, 4 GiB of
memory, 10 pods, and no NodePort or LoadBalancer Service, so a preview cannot
open a port on the nodes. The API server refuses more than 5 replicas.

- Code: [`quota.go`](../internal/controller/quota.go), the `Maximum=5` marker in
  [`previewenvironment_types.go`](../api/v1alpha1/previewenvironment_types.go).
- Tests: `TestReconcileCreatesDefaultLimits`, `TestReconcileCreatesLimitsBeforeDeployment`,
  `TestEnvtestCreatesLimits`, `TestEnvtestAPIValidation`. On a real server, a
  pod over the quota was refused ([try on a server](try-on-a-server.md#6-check-the-isolation-of-previews)).

### A preview is isolated on the network

A NetworkPolicy in each preview namespace ([ADR-0005](adr/0005-network-isolation.md))
lets in only the application port, from the preview itself and from
namespaces that are not previews (the ingress controller). It lets out only
the pods of the preview, DNS, and the public internet: not the private,
link-local and shared address ranges, nor the public addresses of the nodes
and of the API server, which the operator reads and keeps up to date.

- Code: [`networkpolicy.go`](../internal/controller/networkpolicy.go),
  [`hostaddresses.go`](../internal/controller/hostaddresses.go).
- Tests: `TestReconcileCreatesNetworkPolicy`, `TestReconcileRestoresChangedNetworkPolicy`,
  `TestReconcileDeniesHostAddresses`, `TestEnvtestDeniesNewNodeAddress`, and the
  e2e test `TestPreviewNetworkIsolation`, which probes the isolation from inside
  a preview on kind.
- The public addresses of the nodes were first missed:
  [GHSA-m437-fj8p-6x8h](https://github.com/eventide-oss/vesper/security/advisories/GHSA-m437-fj8p-6x8h),
  fixed in v0.1.0-rc.2.

### The objects of a preview exist before it runs, and are restored

The operator creates the quota, the LimitRange and the NetworkPolicy before
the Deployment: no pod of a preview runs without them. If one is changed or
deleted by hand, the operator puts it back. If it cannot write the
NetworkPolicy, or read the addresses it needs, it does not create the
Deployment.

- Code: [`previewenvironment_controller.go`](../internal/controller/previewenvironment_controller.go) (`Reconcile`).
- Tests: `TestReconcileCreatesLimitsBeforeDeployment`, `TestReconcileRestoresChangedQuota`,
  `TestReconcileLeavesForeignNetworkPolicyUntouched` (no Deployment without the
  policy), `TestEnvtestRestoresDeletedObjects`.

### The input is validated before the operator sees it

The CRD schema rejects, on the write path of the API server, even when the
operator does not run: an image that is not pinned with a tag other than
`latest` or a digest, a TTL outside 1 minute to 7 days, replicas outside 1 to
5, a port outside 1 to 65535, environment variable names that are not
identifiers, and names over 63 characters.

- Code: the markers of [`previewenvironment_types.go`](../api/v1alpha1/previewenvironment_types.go).
- Tests: `TestEnvtestAPIValidation`, `TestEnvtestAPIDefaults`.

### Vesper only touches what it created

Preview namespaces are named from the namespace and name of the preview with a
hash, so two previews cannot share one. The operator changes or deletes an
object only if it carries the labels of its preview; it leaves any other
object alone and reports a conflict. Deletions carry a precondition on the UID
of the object, so an object deleted and created again in between is not
removed by mistake.

- Code: [`naming.go`](../internal/controller/naming.go), [`labels.go`](../internal/controller/labels.go),
  [`finalizer.go`](../internal/controller/finalizer.go).
- Tests: `TestNamespaceNameAvoidsCollisions`, `TestReconcileLeavesForeignNamespaceUntouched`,
  `TestReconcileLeavesForeignDeploymentUntouched` and the other `...Foreign...`
  tests, `TestDeletionKeepsForeignNamespace`, `TestEnvtestNamespaceConflict`,
  `TestDeleteSendsUID` (vesperctl).

### A preview does not outlive its TTL

A preview is deleted when its TTL passes, and deleting a preview deletes its
namespace and everything in it, through a finalizer.

- Code: [`ttl.go`](../internal/controller/ttl.go), [`finalizer.go`](../internal/controller/finalizer.go).
- Tests: `TestDeletionDeletesNamespaceThenPreview`, `TestEnvtestExpiresAtFollowsTTL`, the e2e test `TestCLIExpiry`.

### The operator has narrow rights

The ClusterRole of the operator lists only what the code uses. It cannot read
Secrets or ConfigMaps, nor create pods; it reads nodes and EndpointSlices
without writing them. Its own pod meets the `restricted` standard, with a
read-only root filesystem, in a namespace that enforces it.

- Code: the `+kubebuilder:rbac` markers in
  [`previewenvironment_controller.go`](../internal/controller/previewenvironment_controller.go),
  generated into [`config/rbac/role.yaml`](../config/rbac/role.yaml);
  [`config/manager/manager.yaml`](../config/manager/manager.yaml).
- Check: `make manifests` regenerates the role, and CI fails if it drifts
  (`generated` job). The list and the reason for each right are in
  [Development](development.md#operator-permissions).

### HTTPS without private keys in previews

With `--preview-tls`, previews are served over HTTPS with the default
certificate of the ingress controller ([ADR-0006](adr/0006-preview-tls.md)):
no certificate or key is ever written to a preview namespace, and the
operator has no right on Secrets.

- Code: [`ingress.go`](../internal/controller/ingress.go).
- Tests: `TestReconcileServesHTTPS`, `TestEnvtestAcceptsTLSIngress`; on a real
  server, the preview namespace held no Secret
  ([try on a server](try-on-a-server.md#7-serve-previews-over-https)).

### Metrics require authentication

With `config/default`, the metrics endpoint is served over HTTPS and checks
the token of each request with the API server; only a caller allowed by the
ClusterRole `vesper-metrics-reader` reads it. No label holds the name of a
preview.

- Code: [`cmd/main.go`](../cmd/main.go) (`filters.WithAuthenticationAndAuthorization`),
  [`metrics.go`](../internal/controller/metrics.go).
- Check: by hand, as [Metrics](metrics.md#in-a-cluster) describes (401 without
  a token); no automated test covers the authentication.

### The release can be verified

Release assets come with SHA-256 checksums and provenance attestations signed
by the release workflow; `install.yaml` pins the operator image by digest; the
image is distroless and runs as a non-root user. Base images and actions are
pinned by digest or commit SHA.

- Code: [`.github/workflows/release.yml`](../.github/workflows/release.yml),
  [`.goreleaser.yaml`](../.goreleaser.yaml), [`Dockerfile`](../Dockerfile).
- Check: [Releasing](release.md#verify-a-release).

## Known limits

- **A namespace is a soft boundary**, as above. A container escape through a
  kernel or runtime vulnerability is out of reach of every layer here.
  Stronger isolation (gVisor, Kata Containers, one cluster per tenant) is not
  supported yet.
- **The network isolation depends on the CNI.** kindnet, k3s, Calico and
  Cilium enforce NetworkPolicies; flannel alone does not, and the policy is
  then ignored without an error.
- **A preview reaches the internet.** It can call external APIs, and so it can
  also send data out or download code.
- **DNS is open to any address**, on port 53, because the DNS server of the
  cluster has a private address. Data can leave through DNS queries.
- **Preview URLs are public.** Anyone who has the URL opens the application;
  restricting access is planned (#132).
- **Images are not verified.** Vesper checks that an image is pinned, not who
  built it: there is no signature check of preview images.
- **Environment variables are plain text** in the spec of the preview, readable
  by anyone who can read `PreviewEnvironment` objects. Do not put secrets
  there. `vesperctl` hides them in its JSON and YAML output
  (`TestJSONOutputIsValidAndHidesEnv`), but `kubectl` does not.
- **Creating a preview means running any image.** The right to create
  `PreviewEnvironment` objects in a namespace is the right to run code in the
  cluster, within the limits above: grant it as such.
- **A preview cannot call a service of the cluster**, nor a public URL served
  by the same cluster: that is the price of the isolation.

## Hardening checklist

For whoever installs Vesper:

1. **Run previews of untrusted code on a cluster of their own**, not next to
   production workloads.
2. **Use a CNI that enforces NetworkPolicies**, and check it: the e2e test
   `TestPreviewNetworkIsolation` shows how.
3. **Do not expose the API server or the kubelet** to the Internet. On a server,
   allow only 22, 80 and 443, with a final rule that drops the rest
   ([try on a server](try-on-a-server.md#1-install-k3s)).
4. **Grant `create` on `PreviewEnvironment` carefully**: the ClusterRoles
   `vesper-previewenvironment-editor-role` and
   `vesper-previewenvironment-admin-role` give it;
   `vesper-previewenvironment-viewer-role` reads only.
5. **Install a release by digest and verify it** (`gh attestation verify`),
   rather than building or pulling by tag.
6. **Give the metrics endpoint a real certificate** (the `CERTMANAGER`
   sections of `config/default`), and bind `vesper-metrics-reader` only to
   Prometheus.
7. **Keep the preview domain separate** from domains that carry cookies or
   sessions of other applications: a preview serves arbitrary content under it.
8. **Upgrade** when a release fixes a vulnerability: advisories are published
   under [Security advisories](https://github.com/eventide-oss/vesper/security/advisories).
