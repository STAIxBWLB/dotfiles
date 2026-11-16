#!/usr/bin/env bash
# Build CHANGELOG.md from docs/releases/v*.md, newest release first.
#
#   scripts/gen-changelog.sh [out]   default: CHANGELOG.md
#
# The release notes are the source; CHANGELOG.md is generated, never edited.
# Output depends only on the note files (no tags or dates), so CI can check
# drift from a shallow checkout.
set -euo pipefail

out=${1:-CHANGELOG.md}
dir=docs/releases

{
  echo "# Changelog"
  echo
  echo "Generated from \`$dir/\` by \`make changelog\`; do not edit by hand."
  # sort -V puts v2.70.10 after v2.70.9.
  find "$dir" -maxdepth 1 -name 'v*.md' -exec basename {} .md \; | sort -rV |
    while IFS= read -r tag; do
      echo
      echo "## $tag"
      echo
      # $(...) strips every trailing newline; printf adds exactly one back.
      printf '%s\n' "$(cat "$dir/$tag.md")"
    done
} >"$out"
