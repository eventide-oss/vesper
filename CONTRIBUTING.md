# Contributing to Vesper

Thank you for your interest in Vesper. This page explains how to propose a
change and what a pull request must contain. By taking part, you agree to follow
the [Code of Conduct](CODE_OF_CONDUCT.md). Decisions are described in
[GOVERNANCE.md](GOVERNANCE.md).

Everything in the repository and on GitHub is written in **English**: code,
comments, commit messages, issues, pull requests and documentation.

## Ways to contribute

- Report a bug or suggest a feature by opening an issue.
- Pick an issue labelled
  [`good first issue`](../../labels/good%20first%20issue) or
  [`help wanted`](../../labels/help%20wanted).
- Improve the documentation or add tests.
- To report a **security vulnerability**, do not open a public issue: follow
  `SECURITY.md`.

## Set up

See [docs/development.md](docs/development.md) for the tools you need (Go,
Docker, kind, kubectl) and for the local cluster (`make kind-up`).

```sh
make build   # build vesperctl into bin/
make test    # go test -race ./..., with envtest
make vet     # go vet ./...
make lint    # golangci-lint run
```

## Workflow

1. **Start from an issue.** Every change starts from an issue with four parts:
   Context, Problem, Proposed solution, Acceptance criteria. If none exists,
   open one and wait for a maintainer to agree before you write code. One issue
   maps to one pull request.
2. **Create a branch from an up-to-date `main`**, named
   `<type>/<issue-number>-<short-description>`, where type is `feat`, `fix` or
   `chore` (for example `feat/42-ttl-expiry`).
3. **Commit small and often**, using
   [Conventional Commits](https://www.conventionalcommits.org/en/v1.0.0/)
   (`feat(cli): add list command`). Each commit builds and passes the tests.
4. **Sign off every commit** (see below).
5. **Open a draft pull request** that follows the
   [template](.github/PULL_REQUEST_TEMPLATE.md), with `Closes #<n>`. Keep it
   small: one logical change, ideally under 400 changed lines (generated code
   excluded). Mark it ready when CI is green.
6. A maintainer reviews, and merges with a **squash**. The pull request title
   becomes the commit message, so it must follow Conventional Commits too.

Labels come from [`.github/labels.yml`](.github/labels.yml): exactly one `type/`
label and at least one `area/` label per pull request. A maintainer applies
them if you cannot.

## Developer Certificate of Origin (DCO)

Vesper uses the [DCO](https://developercertificate.org/) instead of a CLA: you
certify that you wrote the change or have the right to submit it under the
project license. Add a sign-off to each commit:

```sh
git commit -s -m "feat(cli): add list command"
```

This appends `Signed-off-by: Your Name <you@example.com>`, using the name and
email from your git configuration. The email must be the commit's author or
committer email. To fix commits that miss it:

```sh
git commit --amend --signoff --no-edit     # last commit
git rebase --signoff origin/main           # every commit of the branch
git push --force-with-lease
```

## Before you push

- Run `make vet`, `make test` and `make lint`.
- Go through the [security checklist](.claude/skills/security-review/SKILL.md).
  It matters here: Vesper creates workloads for users inside a shared cluster.
- Never commit secrets, kubeconfigs, `.env` files or build artifacts.

## Dependency updates

Dependabot opens weekly pull requests for Go modules and GitHub Actions
(`.github/dependabot.yml`). Review them like any other pull request: CI must be
green and the release notes read. Actions stay pinned to a full commit SHA; the
bot updates the SHA and the version comment together.

## Working with AI agents

Maintainers may use AI coding agents. Their additional rules, such as the
`Co-authored-by` trailer, are in [AGENTS.md](AGENTS.md). They never lift a
rule above: an agent does not push to `main` and does not merge.

## License

By contributing, you agree that your contributions are licensed under the
[Apache License 2.0](LICENSE).
