# ADR-0002: Run each preview in a namespace of its own, tracked by labels

- **Status:** Accepted
- **Date:** 2026-11-06
- **Deciders:** @damnthonyy
- **Issue / PR:** #37 (implemented in #30, #31, #32 and #33)

## Context

A preview runs one version of an application: a Deployment and a Service
today, then an Ingress, a ResourceQuota, a LimitRange and NetworkPolicies
(P3). Many previews run side by side in one shared cluster, created by
different teams, and each must disappear when its TTL expires.

Constraints that matter:

- **Isolation.** A preview must not see or break the others, nor the other
  workloads of the cluster. The Kubernetes tools for this are scoped to a
  namespace: Pod Security admission (a namespace label), ResourceQuota,
  LimitRange and the default of a NetworkPolicy.
- **Names.** Every preview of a given application has the same objects
  (`app`, its Service, its Ingress). Two previews must never collide.
- **Cleanup.** When a preview is deleted or expires, everything it created must
  go, without leaving orphans and without touching anything else.
- **Safety.** The operator must never modify or delete an object it did not
  create (see the security review checklist).

## Decision

We give **each preview a namespace of its own**, created by the operator and
named `vesper-<preview namespace>-<preview name>-<hash>` (at most 63
characters, with an 8-character hash of `<namespace>/<name>` so that two
previews never share it). The namespace enforces the `restricted` Pod Security
profile, and every object of the preview lives in it under fixed names
(`app`).

The objects are tied to their preview by **three labels, not owner
references**: `app.kubernetes.io/managed-by=vesper`,
`vesper.eventide.dev/preview-namespace` and `vesper.eventide.dev/preview-name`.
An owner reference cannot work here: the preview lives in the user's namespace
and its objects in the preview namespace, and an owner reference cannot point
to another namespace; the preview namespace itself is cluster-scoped and
cannot have a namespaced owner. The operator checks the labels before it
modifies an object, and reports `NamespaceConflict` for a namespace it did not
create. **The namespace is the cleanup boundary**: deleting it deletes
everything in it, which a finalizer on the preview will do (#39).

## Alternatives considered

**All previews in one shared namespace** (for example `vesper-previews`).
Fewer namespaces, no permission to create namespaces, and the objects could
have the preview as owner if the previews lived there too, so the garbage
collector would clean up. But every object name needs a preview prefix,
quotas and network rules must be written per preview with label selectors
(a ResourceQuota cannot be scoped to one preview), one Pod Security level
applies to all, and a preview can reach every other preview by default. A
mistake in a selector affects all previews at once.

**Each preview in the namespace of its `PreviewEnvironment`** (next to the
object, in the team's namespace). The simplest model: owner references and
garbage collection work, and teams see their previews with their usual
access. But the preview mixes with the team's other workloads; enforcing the
`restricted` profile, a quota or a default-deny NetworkPolicy would apply to
the team's whole namespace, which Vesper must not change; names still collide
between two previews; and the operator needs write access to every team
namespace anyway.

**A virtual cluster or a cluster per preview** (for example vcluster or kind
in the cluster). The strongest isolation, but each preview becomes much
heavier and slower to start, and the operator would manage clusters instead of
a few objects. It is far beyond the scope of v0.1; a namespace is enough for
trusted teams sharing a cluster.

## Consequences

- Quotas, limits, network policies and Pod Security apply per preview with the
  standard namespace-scoped objects (P3).
- The operator needs a ClusterRole that can create namespaces. This is a
  powerful permission: the operator only ever modifies namespaces that carry
  the labels of the preview, and never deletes one without them.
- Without owner references, the operator must clean up itself: deleting the
  preview namespace on deletion and on TTL expiry needs a finalizer (#39). An
  operator that stops while a preview is deleted leaves its namespace until it
  runs again.
- Label values are at most 63 characters, so preview names are limited to 63
  characters by a validation rule.
- A namespace is a soft boundary. Vesper does not promise hard multi-tenant
  isolation: previews share nodes, the kernel and the cluster DNS.
- We revisit this decision if the number of namespaces becomes a problem for
  the cluster, or if previews need hard isolation (then a virtual cluster per
  preview becomes worth its cost).
