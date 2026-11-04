# ADR-0001: Use Kubebuilder (controller-runtime) to build the operator

- **Status:** Accepted
- **Date:** 2026-11-04
- **Deciders:** @damnthonyy
- **Issue / PR:** #17

## Context

Vesper is a Kubernetes operator: it watches a custom resource
(`PreviewEnvironment`) and reconciles the cluster toward the state it describes.
Writing an operator means choosing how much to build on a framework.

Constraints that matter for this project:

- The API (the CRD) is the public surface. Its schema, validation and RBAC must
  stay consistent with the Go types, and that is easy to get wrong by hand.
- Vesper is also a learning project: the maintainer wants to understand
  controllers, reconciliation, finalizers and owner references, not hide them.
- The scope of v0.1 is small (deploy one image per pull request). The tooling
  must not add more concepts than the problem has.
- The code is written in Go and licensed under Apache-2.0.

## Decision

We use **Kubebuilder**: the project is scaffolded with `kubebuilder init`, the
API with `kubebuilder create api`, and the controller is written against
**controller-runtime**. CRD manifests, RBAC rules and deep-copy code are generated
from Go types and `+kubebuilder:` markers.

## Alternatives considered

**Operator SDK (Go plugin).** A Go operator created with Operator SDK is
scaffolded with Kubebuilder's plugin system, so the resulting code is close to
ours. Operator SDK adds integration with the Operator Lifecycle Manager (OLM),
bundles and a scorecard, and Ansible and Helm operators. We do not need these
for v0.1, and they add concepts and files we would not use. Because both share
controller-runtime, we can adopt the OLM parts later without rewriting the
controllers.

**Plain client-go (informers, work queues).** Maximum control and the clearest
view of how a controller works, but we would write and maintain the boilerplate
that controller-runtime provides: caches, event handlers, rate-limited queues,
leader election, metrics and health endpoints, and test helpers such as
envtest integration. For a small team and a small scope this is risk without
benefit. It remains a good topic for the learning journal.

**A higher-level tool (for example a generic controller framework or a Helm
based operator).** Faster to start, but the reconciliation logic would live
outside Go code that we control and test. That conflicts with our need to
validate input, enforce ownership and run envtest tests on the controller.

## Consequences

- We follow Kubebuilder's project layout and conventions (`api/`,
  `internal/controller/`, `config/`, `Makefile` targets). New contributors who
  know Kubebuilder find their way quickly.
- We depend on controller-runtime and its release cadence. Upgrading it is a
  routine task, handled by dependency updates.
- Generated files (CRD, RBAC, deep copy) must be regenerated after each API
  change. CI will fail when they are out of date (planned in the operator
  milestone).
- We write envtest-based tests, which controller-runtime supports directly.
- We revisit this decision if we need OLM-based distribution (then Operator SDK
  is a small step) or if controller-runtime stops fitting our needs.
