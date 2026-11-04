# Governance

Vesper is a small project. This page says who decides and how, so that the
process stays clear as the project grows.

## Roles

- **Contributor**: anyone who opens an issue, a pull request, or helps in a
  discussion. No permission is needed. Contributors follow
  [CONTRIBUTING.md](CONTRIBUTING.md).
- **Maintainer**: can triage issues, review, and merge pull requests, and is
  responsible for releases. Maintainers also enforce the
  [Code of Conduct](CODE_OF_CONDUCT.md).

Current maintainers:

| Name | GitHub |
| --- | --- |
| Antoine Mahassadi | [@damnthonyy](https://github.com/damnthonyy) |

**Conduct contact:** to be defined before the repository is made public (the
private address for Code of Conduct reports).

## Decision making

- **Everyday changes** (bug fixes, small features, documentation) are decided in
  the issue and the pull request. A maintainer's approval is enough.
- **Significant changes** (public API such as the CRD, security model,
  dependencies with a large footprint, project direction) are written down as an
  Architecture Decision Record in `docs/adr/` and discussed in the pull request
  that adds it. We aim for consensus; when there is none, the maintainers
  decide, and the record states the alternatives that were rejected and why.
- **Merging**: a pull request is merged by a maintainer other than its author
  when the project has more than one. With a single maintainer, the author
  merges only once CI is green. AI agents never merge.
- **Releases** follow [Semantic Versioning](https://semver.org/). Before 1.0, a
  minor version may contain breaking changes, announced in the release notes.

## Becoming a maintainer

Contributors who have sent several substantial pull requests, reviewed others'
work with care, and followed the Code of Conduct can be invited by the current
maintainers. Maintainers who are inactive for a long period are asked whether
they want to step down; they can come back at any time.

## Changing this document

Changes to this page follow the same process as a significant change: a pull
request, discussed openly, approved by the maintainers.
