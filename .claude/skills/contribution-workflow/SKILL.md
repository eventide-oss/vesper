---
name: contribution-workflow
description: How to work on Vesper. Start from an issue, name branches, write Conventional Commits with the Co-authored-by trailer, keep pull requests small, follow the PR template, and apply labels. Use before creating a branch, committing, or opening a pull request.
---

# Contribution workflow

Follow every step, in order, for every change. Everything you write in the
repository or on GitHub is in English (see `AGENTS.md`).

## 1. Start from an issue

- Every change starts from a GitHub issue. No issue, no branch.
- If no issue exists, draft one with four parts (Context, Problem, Proposed
  solution, Acceptance criteria) and ask a maintainer to approve it before you
  create a branch.
- One issue maps to one pull request. If an issue is too large for a small
  pull request, split it into smaller issues first.

## 2. Branch naming

Format: `<type>/<issue-number>-<short-description>`

| Prefix | Use it for | Label to apply |
| --- | --- | --- |
| `feat/` | New capability or behavior | `type/feature` |
| `fix/` | Bug fix | `type/bug` |
| `chore/` | Everything else: docs, tests, refactor, CI, dependencies, tooling | `type/chore` |

- The description is lowercase kebab-case, at most five words.
- Good: `feat/42-ttl-expiry`, `fix/57-namespace-leak`, `chore/61-add-envtest`.
- Bad: `my-branch`, `feat/ttl`, `Feat/42_TTL_Expiry`, `fix-bug`.
- Always branch from an up-to-date `main`. Never create a branch "at random".

## 3. Commits

Commit early and often. Each commit is small, does one thing, and leaves the
build and the tests passing.

Format ([Conventional Commits 1.0.0](https://www.conventionalcommits.org/en/v1.0.0/)):

```
<type>(<scope>)<!>: <description>

<optional body>

<optional footers>

Signed-off-by: Antoine Mahassadi <127432580+damnthonyy@users.noreply.github.com>
Co-authored-by: Antoine Mahassadi <127432580+damnthonyy@users.noreply.github.com>
```

- **Types:** `feat`, `fix`, `docs`, `style`, `refactor`, `perf`, `test`,
  `build`, `ci`, `chore`, `revert`. `feat` and `fix` are the ones the
  specification requires; the others are conventional. The commit type is
  independent of the branch prefix: a `chore/` branch can hold `docs:`,
  `test:` or `refactor:` commits.
- **Scope** (optional): `api`, `controller`, `lifecycle`, `cli`, `networking`,
  `security`, `observability`, `integrations`, `deps`, `ci`, `docs`.
- **Description:** imperative mood ("add", not "added"), lowercase, no trailing
  period, 72 characters or fewer on the first line.
- **Body:** optional, wrapped at 72 characters. Explain what and why, not how.
- **Footers:** `Closes #<n>` (or `Refs #<n>`). Breaking changes add `!` before
  the colon and a `BREAKING CHANGE: <description>` footer.
- **Trailers:** every commit ends, after a blank line, with both trailers above.
  `Signed-off-by` is the sign-off required by the
  [Developer Certificate of Origin](https://developercertificate.org/) (DCO):
  no commit without it (`git commit -s` adds it for the committing identity).
  `Co-authored-by` credits the maintainer. Do not change the name or the
  email: GitHub attributes the co-author through the email linked to the
  account. Trailers stay together in the last block of the message.

Example:

```
feat(lifecycle): delete environment when its TTL expires

The controller requeues each PreviewEnvironment until spec.ttl elapses,
then deletes its namespace. A finalizer guarantees the cleanup runs.

Closes #42

Signed-off-by: Antoine Mahassadi <127432580+damnthonyy@users.noreply.github.com>
Co-authored-by: Antoine Mahassadi <127432580+damnthonyy@users.noreply.github.com>
```

## 4. Pull requests

- **Small and easy to test.** One logical change per pull request. Aim for
  fewer than 400 changed lines, not counting generated code (`zz_generated*`,
  generated CRD and RBAC manifests, `go.sum`). If it is larger, split it.
- **Title:** Conventional Commit format, 72 characters or fewer. It becomes the
  squash commit message.
- **Description:** you **must** follow `.github/PULL_REQUEST_TEMPLATE.md`.
  Read it each time. Keep every section; fill each one briefly; write "N/A"
  with a reason when a section does not apply. Never write free-form
  descriptions.
- **Issue link:** `Closes #<n>` in the "Linked issue" section.
- **Labels:** exactly one `type/` label and at least one `area/` label, taken
  from `.github/labels.yml`. Add `breaking-change` when it applies. Never
  invent a label; if none fits, ask a maintainer.
- **Draft first.** Open as draft; mark ready only when CI is green and the
  checklist is complete.
- **Before every push:** run the project checks (`make test` and `make lint`
  once they exist; until then `go vet ./...` and `go test -race ./...`), then
  run `.claude/skills/security-review/SKILL.md`.
- **Merging** is done by a maintainer, with a squash merge. Agents never merge.

## 5. Never

- Never push to `main` or force-push a shared branch.
- Never mix unrelated changes in one pull request.
- Never skip hooks (`--no-verify`) or checks.
- Never commit secrets, kubeconfigs, `.env` files or build artifacts.
