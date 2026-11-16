#!/usr/bin/env bash
# Fail the build when a shared UI marker is redefined as an inline Go literal,
# embedded in a longer literal, or when a warning/note/tip/dry-run prefix is
# hand-written outside internal/ui (scripts/check-output-literals.go).
set -euo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(cd -- "$script_dir/.." && pwd)

alphabet_file="$repo_root/internal/ui/markers.go"
# MarkPending's "~" is deliberately excluded: nine measured, legitimate bare
# tilde path literals exist in this tree, so including it would make this gate
# permanently red on correct code.
glyph_pattern='(["`])[✓✗·★◆⚠]\1'

go_files=()
while IFS= read -r file; do
  go_files+=("$file")
done < <(find "$repo_root" -type f -name '*.go' -print | LC_ALL=C sort)

# This precondition is deliberately before the definition file is excluded
# below. If the pattern no longer recognizes the alphabet it is meant to
# enforce, a green verdict would be success having measured nothing.
if ! rg -q -P "$glyph_pattern" "$alphabet_file"; then
  echo "check-glyph-alphabet: pattern does not match $alphabet_file; would otherwise report success having measured nothing" >&2
  exit 1
fi

if [[ ${#go_files[@]} -eq 0 ]]; then
  echo "check-glyph-alphabet: no Go files found, so this check would report success without measuring anything" >&2
  exit 1
fi

echo "check-glyph-alphabet: scanned ${#go_files[@]} Go file(s) against internal/ui/markers.go"

findings=0

# Accumulate every finding so a single run exposes the complete residue rather
# than making the next violation visible only after another CI cycle.
for file in "${go_files[@]}"; do
  if [[ "$file" == "$alphabet_file" ]]; then
    continue
  fi

  if matches=$(rg -n -P "$glyph_pattern" -- "$file"); then
    relative_file=${file#"$repo_root/"}
    while IFS=: read -r line source; do
      echo "check-glyph-alphabet: $relative_file:$line:$source" >&2
      findings=$((findings + 1))
    done <<<"$matches"
  fi
done

# Marker glyphs embedded in longer strings, the bullet and info glyphs, and
# hand-written warning/note/tip/dry-run prefixes need a tokenizer rather than a
# line pattern, so a Go program measures them. It runs even when the bare-glyph
# pass above found something, so one run shows the whole residue.
output_status=0
(cd "$repo_root" && go run scripts/check-output-literals.go -root "$repo_root") || output_status=$?

if [[ "$findings" -gt 0 ]]; then
  echo "check-glyph-alphabet: inline glyph literal(s); use the internal/ui Mark* constants" >&2
  echo "check-glyph-alphabet: $findings finding(s)" >&2
  exit 1
fi
if [[ "$output_status" -ne 0 ]]; then
  exit "$output_status"
fi

echo "check-glyph-alphabet: ${#go_files[@]} Go file(s) measured; no inline glyph literals outside internal/ui/markers.go"
