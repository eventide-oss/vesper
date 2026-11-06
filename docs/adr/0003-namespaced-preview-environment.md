# ADR-0003: Make PreviewEnvironment a namespaced resource

- **Status:** Accepted
- **Date:** 2026-11-06
- **Deciders:** @damnthonyy
- **Issue / PR:** #37 (implemented in #27 and #28)

## Context

A custom resource is either **namespaced** (each object lives in a namespace,
like a Deployment) or **cluster-scoped** (one object for the whole cluster,
like a Namespace). The scope is set when the CRD is created and cannot change
afterwards without deleting every object: the decision has to be made before
the first release.

Constraints that matter:

- Previews are created by the CI of each team, and later by the GitHub
  integration, often in a cluster shared by several teams.
- A team must be able to create, read and delete its own previews without
  seeing or changing the previews of other teams.
- Access in Kubernetes is granted with RBAC: a Role and a RoleBinding grant
  access to the objects of one namespace; a ClusterRole bound with a
  ClusterRoleBinding grants it on every object of a cluster-scoped type.
- Two teams can both have a pull request #42.

## Decision

`PreviewEnvironment` is a **namespaced** resource (`scope: Namespaced`, short
name `pe`). A team creates its previews in its own namespace, and is given
access to them with a standard Role and RoleBinding. The operator watches the
previews of every namespace.

## Alternatives considered

**Cluster-scoped `PreviewEnvironment`.** One list of previews for the whole
cluster: `kubectl get pe` shows everything, and a preview needs only its name
to be identified. But RBAC cannot restrict a cluster-scoped type per team: a
CI that can create previews can also delete those of every other team. The
names of all teams share one space, so two `pr-42` collide unless every team
prefixes its names by convention.

**Cluster-scoped with a team field** (`spec.team`), checked by an admission
webhook or by the operator. It would keep one list and restore some
separation, but it reimplements in Vesper what Kubernetes RBAC already does,
with a webhook to run and secure, and a mistake in it would give a team access
to the others' previews.

## Consequences

- Teams use the access model they already know: `kubectl -n <namespace>`, Roles
  and RoleBindings. `vesperctl` works in the current namespace, or in all
  namespaces with `-A`.
- A preview is identified by its namespace and its name. Both appear in the
  name of its preview namespace and in the labels of its objects, which is why
  the preview namespace name is shortened and carries a hash (ADR-0002).
- Its objects live in another namespace, so they cannot have the preview as
  owner: they are tracked by labels instead (ADR-0002).
- The operator reads previews in every namespace, which needs a ClusterRole
  limited to `get`, `list` and `watch` on `previewenvironments` and `patch` on
  their status.
- We revisit this decision only if previews must be managed by a single
  platform team rather than by each team; changing the scope would require a
  new API version and a migration.
