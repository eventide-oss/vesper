# Agent instructions for Vesper

Vesper is a Kubernetes operator and CLI (`vesperctl`) that creates ephemeral
preview environments, one per pull request. It is written in Go and licensed
under Apache-2.0.

## Read before you work

1. `.claude/skills/contribution-workflow/SKILL.md`: issues, branches, commits,
   pull requests and labels. **Mandatory for every change.**
2. `.claude/skills/security-review/SKILL.md`: **mandatory before every push.**
3. `.github/PULL_REQUEST_TEMPLATE.md`: every pull request description must
   follow it. Read it before writing a description.
4. `.github/labels.yml`: the only labels you may apply.

## Language

- Write everything in English: code, comments, identifiers, commit messages,
  branch names, issues, pull request titles and descriptions, documentation
  and labels.
- The maintainer may talk to you in French. Reply in the language the
  maintainer uses, and switch language whenever asked. This never changes the
  language of anything written in the repository or on GitHub.

## Ground rules

- Never work without an issue. Never push to `main`. Never force-push a
  shared branch. Never merge your own pull request.
- Sign off every commit (DCO) and add the co-author trailer, as described in
  the workflow skill.
- Keep changes small and testable.
- When you introduce a Go or Kubernetes concept, explain what it is and why
  you chose it, in the chat, before using it.
- When a rule is unclear, or two rules conflict, stop and ask a maintainer.
