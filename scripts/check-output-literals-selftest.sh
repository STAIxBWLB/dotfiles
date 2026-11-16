#!/usr/bin/env bash
# Prove scripts/check-output-literals.go fails on each planted literal class and
# passes on clean input. A gate that cannot fail certifies nothing, so this runs
# next to the gate in CI.
set -euo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(cd -- "$script_dir/.." && pwd)
checker="$repo_root/scripts/check-output-literals.go"

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

fail() {
  echo "check-output-literals-selftest: $*" >&2
  exit 1
}

# fixture <name> <ui-markers-file-or-empty> -> prints the fixture root.
fixture() {
  local root="$work/$1"
  mkdir -p "$root/internal/ui" "$root/internal/cli"
  cp "$repo_root/internal/ui/markers.go" "$root/internal/ui/markers.go"
  printf 'package cli\n' >"$root/internal/cli/ok.go"
  echo "$root"
}

run_checker() {
  (cd "$repo_root" && go run "$checker" -root "$1") >"$work/out.log" 2>&1
}

expect_fail() {
  local name=$1 body=$2 root
  root=$(fixture "$name")
  printf 'package cli\n\nvar x = %s\n' "$body" >"$root/internal/cli/planted.go"
  if run_checker "$root"; then
    cat "$work/out.log" >&2
    fail "planted literal '$name' was not rejected"
  fi
  grep -q 'planted.go' "$work/out.log" || fail "planted literal '$name' failed without naming planted.go"
}

expect_pass() {
  local name=$1 file=$2 body=$3 root
  root=$(fixture "$name")
  mkdir -p "$(dirname "$root/$file")"
  printf '%s\n' "$body" >"$root/$file"
  if ! run_checker "$root"; then
    cat "$work/out.log" >&2
    fail "clean input '$name' was rejected"
  fi
}

# Glyphs are written as printf escapes so this file stays free of the literals.
check=$(printf '\xe2\x9c\x93')
warn=$(printf '\xe2\x9a\xa0')
bullet=$(printf '\xe2\x80\xa2')
info=$(printf '\xe2\x84\xb9')

expect_fail embedded-check "\"$check done\""
expect_fail rune-warn "'$warn'"
expect_fail bullet "\"  $bullet item\""
expect_fail info "\"$info note\""
expect_fail warning-lower '"warning: disk"'
expect_fail warning-upper '"Warning: disk"'
expect_fail note-upper '"Note: disk"'
expect_fail note-lower '"note: disk"'
expect_fail tip '"Tip: disk"'
expect_fail dry-run '"[dry-run] would write"'
expect_fail raw-multiline '`first line
Note: second line`'

expect_pass comment internal/cli/c.go "package cli

// $check done, $bullet, warning: and [dry-run] in a comment are not output.
var _ = 1"
expect_pass ui-owned internal/ui/own.go "package ui

var _ = \"$check done\" + \"warning: x\""
expect_pass test-file internal/cli/c_test.go "package cli

var _ = \"Note: $bullet\""
expect_pass plain-word internal/cli/w.go 'package cli

var _ = "no warning here"'

# The alphabet precondition: a marker glyph the gate does not know about fails.
root=$(fixture alphabet-drift)
sed -i.bak 's/markerSection: {"section", "[^"]*"/markerSection: {"section", "\xe2\x98\x86"/' "$root/internal/ui/markers.go"
rm -f "$root/internal/ui/markers.go.bak"
if run_checker "$root"; then
  cat "$work/out.log" >&2
  fail "a marker glyph missing from the gate was not rejected"
fi
grep -q 'markerGlyphs does not cover' "$work/out.log" || fail "alphabet drift failed for the wrong reason"

# The real tree is green.
if ! run_checker "$repo_root"; then
  cat "$work/out.log" >&2
  fail "the repository itself is rejected"
fi

echo "check-output-literals-selftest: every planted literal was rejected; clean input and the repository pass"
