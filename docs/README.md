# Documentation

- [Quickstart](quickstart.md): run Vesper on kind in ten minutes, from a
  release.
- [Cheat sheet](cheatsheet.md): the everyday commands, by task, with what
  each flag does.
- [vesperctl reference](cli.md): commands, output formats, exit codes and
  error messages.
- [Architecture](architecture.md): the pieces of Vesper, a reconciliation
  step by step, and the life of a preview.
- [CRD reference](crd-reference.md): every field of `PreviewEnvironment`,
  generated from the Go types (`make docs-reference`).
- [Development](development.md): tools and the local kind cluster.
- [Try Vesper on a server](try-on-a-server.md): k3s on a VPS, a wildcard DNS
  record and HTTPS with cert-manager, step by step.
- [Security model](security.md): what Vesper protects against, its known
  limits, and a hardening checklist.
- [Metrics](metrics.md): the Prometheus metrics of the operator, and how to
  read them.
- [Releasing](release.md): how to cut a release, and how to verify one.
- [Acceptance of v0.1](acceptance-v0.1.md): v0.1 checked against the
  criteria of the framing letter, and how to run the check again.
- [Architecture Decision Records](adr/): why we made the important decisions.
  Start from [the template](adr/0000-template.md).
  - [ADR-0001: Use Kubebuilder](adr/0001-use-kubebuilder.md)
  - [ADR-0002: Run each preview in a namespace of its own, tracked by labels](adr/0002-namespace-per-preview.md)
  - [ADR-0003: Make PreviewEnvironment a namespaced resource](adr/0003-namespaced-preview-environment.md)
  - [ADR-0004: Count the TTL of a preview from its creation](adr/0004-ttl-semantics.md)
  - [ADR-0005: Isolate each preview on the network](adr/0005-network-isolation.md)
  - [ADR-0006: Serve previews over HTTPS with the default certificate of the ingress controller](adr/0006-preview-tls.md)
- [Learning journal](journal/): notes after each work session, from
  [the template](journal/TEMPLATE.md).

## Writing an ADR

Write one when a decision is hard to reverse or shapes the design: the public
API, the security model, a framework or a large dependency. Copy the template to
`docs/adr/NNNN-short-title.md` with the next number, set the status to
`Proposed`, and open a pull request. The discussion happens in that pull
request. The status becomes `Accepted` when it is merged.
