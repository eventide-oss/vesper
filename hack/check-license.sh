#!/usr/bin/env bash
# Fail if a tracked Go file does not start with the license header of the
# project (hack/boilerplate.go.txt): an SPDX identifier in its first lines.
set -euo pipefail

missing=()
while IFS= read -r file; do
	if ! head -n 5 -- "$file" | grep -q '^// SPDX-License-Identifier: Apache-2.0$'; then
		missing+=("$file")
	fi
done < <(git ls-files '*.go')

if ((${#missing[@]} > 0)); then
	echo "These Go files have no license header. Start them with:" >&2
	echo >&2
	sed 's/YEAR/'"$(date +%Y)"'/' hack/boilerplate.go.txt >&2
	echo >&2
	printf '  %s\n' "${missing[@]}" >&2
	exit 1
fi
echo "Every Go file has a license header."
