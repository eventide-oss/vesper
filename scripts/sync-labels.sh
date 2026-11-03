#!/usr/bin/env bash
# Create or update the repository labels defined in .github/labels.yml.
# Requires the GitHub CLI (gh), authenticated, run from inside the repository.
set -euo pipefail

file="$(dirname "$0")/../.github/labels.yml"

awk '
  /^- name:/        { sub(/^- name: */, ""); name = $0 }
  /^  color:/       { gsub(/^  color: *"|"$/, ""); color = $0 }
  /^  description:/ { sub(/^  description: */, ""); print name "\t" color "\t" $0 }
' "$file" | while IFS=$'\t' read -r name color desc; do
  echo "label: ${name}"
  gh label create "${name}" --color "${color}" --description "${desc}" --force
done
