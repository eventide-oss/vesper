# Documentation

- [vesperctl reference](cli.md): commands, output formats, exit codes and
  error messages.
- [Development](development.md): tools and the local kind cluster.
- [Architecture Decision Records](adr/): why we made the important decisions.
  Start from [the template](adr/0000-template.md); the first record is
  [ADR-0001: Use Kubebuilder](adr/0001-use-kubebuilder.md).
- [Learning journal](journal/): notes after each work session, from
  [the template](journal/TEMPLATE.md).

## Writing an ADR

Write one when a decision is hard to reverse or shapes the design: the public
API, the security model, a framework or a large dependency. Copy the template to
`docs/adr/NNNN-short-title.md` with the next number, set the status to
`Proposed`, and open a pull request. The discussion happens in that pull
request. The status becomes `Accepted` when it is merged.
