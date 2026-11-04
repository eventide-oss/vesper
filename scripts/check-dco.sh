#!/usr/bin/env bash
# Check that every commit in a range carries a valid DCO sign-off.
#
# Usage: scripts/check-dco.sh <base-sha> <head-sha>
#
# A commit passes when it has a line "Signed-off-by: Name <email>" whose email
# is the author's or the committer's. Merge commits and commits made by bots
# (author email ending in "[bot]@users.noreply.github.com") are skipped.
set -euo pipefail

if [ "$#" -ne 2 ]; then
  echo "usage: $0 <base-sha> <head-sha>" >&2
  exit 2
fi
base=$1
head=$2

failed=0
checked=0

while IFS= read -r sha; do
  author_email=$(git log -1 --format=%ae "$sha" | tr '[:upper:]' '[:lower:]')
  committer_email=$(git log -1 --format=%ce "$sha" | tr '[:upper:]' '[:lower:]')
  subject=$(git log -1 --format=%s "$sha")

  case "$author_email" in
    *"[bot]@users.noreply.github.com") continue ;;
  esac
  checked=$((checked + 1))

  # Emails of every well-formed sign-off line in the commit message.
  signoffs=$(git log -1 --format=%B "$sha" \
    | sed -nE 's/^Signed-off-by: .+ <([^<>[:space:]]+@[^<>[:space:]]+)>[[:space:]]*$/\1/p' \
    | tr '[:upper:]' '[:lower:]')

  if [ -z "$signoffs" ]; then
    echo "::error::${sha:0:7} \"$subject\": no Signed-off-by line"
    failed=1
  elif ! printf '%s\n' "$signoffs" | grep -qxF -e "$author_email" -e "$committer_email"; then
    echo "::error::${sha:0:7} \"$subject\": Signed-off-by does not match the author ($author_email) or the committer ($committer_email)"
    failed=1
  fi
done < <(git rev-list --no-merges "$base..$head")

if [ "$failed" -ne 0 ]; then
  cat >&2 <<'MSG'

Every commit must be signed off to certify the Developer Certificate of Origin
(https://developercertificate.org). To fix:
  - last commit:        git commit --amend --signoff --no-edit
  - several commits:    git rebase --signoff origin/main
  then push with:       git push --force-with-lease
MSG
  exit 1
fi

echo "DCO check passed ($checked commit(s) checked)."
