#!/usr/bin/env bash
# Check release notes against the AGENTS.md "Release notes" format.
#
#   scripts/check-release-notes.sh [file...]   default: every docs/releases/v*.md
#   scripts/check-release-notes.sh --self-test
#
# Checks format only, not wording: the first line is a plain sentence, the
# only headings are the allowed ### sections in order, no 40-hex SHA or
# conventional-commit prefix appears, and the last line is the changelog link
# for the file's own tag. The previous release is the previous note in version
# order in the file's directory; the oldest note links /commits/<tag>.
set -uo pipefail

REPO_URL="https://github.com/STAIxBWLB/dotfiles"
SECTIONS=("### New" "### Improved" "### Fixed" "### Action needed")

err() { echo "$1:$2: $3"; bad=1; }

# Prints the tag released before $1 (a vX.Y.Z.md path), or nothing.
previous_tag() {
  local dir tag
  dir=$(dirname "$1"); tag=$(basename "$1" .md)
  # sort -V puts v2.70.10 after v2.70.9.
  find "$dir" -maxdepth 1 -name 'v*.md' -exec basename {} .md \; | sort -V |
    awk -v t="$tag" '$0 == t { print prev; exit } { prev = $0 }'
}

check_file() {
  local f=$1 tag prev want n=0 line last_section=-1 last=""
  tag=$(basename "$f" .md)
  if [[ ! "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then err "$f" 0 "file name is not vX.Y.Z.md"; return; fi
  if [ ! -s "$f" ]; then err "$f" 0 "empty"; return; fi
  while IFS= read -r line || [ -n "$line" ]; do
    n=$((n + 1))
    last=$line
    if [ "$n" = 1 ]; then
      case "$line" in
        "" | "#"* | "-"* | "*"*) err "$f" 1 "first line must be a plain sentence" ;;
      esac
    fi
    if [[ "$line" == "#"* ]]; then
      local i found=-1
      for i in "${!SECTIONS[@]}"; do [ "$line" = "${SECTIONS[$i]}" ] && found=$i; done
      if [ "$found" -lt 0 ]; then
        err "$f" "$n" "heading must be one of: ${SECTIONS[*]}"
      elif [ "$found" -le "$last_section" ]; then
        err "$f" "$n" "section out of order or repeated: $line"
      else
        last_section=$found
      fi
    fi
    if [[ "$line" =~ (^|[^0-9a-f])[0-9a-f]{40}([^0-9a-f]|$) ]]; then err "$f" "$n" "commit SHA"; fi
    if [[ "$line" =~ ^(-[[:space:]]+)?(feat|fix|chore|docs|ci|refactor|test|perf|build|style|revert)(\([^\)]*\))?!?:[[:space:]] ]]; then
      err "$f" "$n" "conventional-commit prefix"
    fi
  done <"$f"
  prev=$(previous_tag "$f")
  if [ -n "$prev" ]; then
    want="**Full changelog**: $REPO_URL/compare/$prev...$tag"
  else
    want="**Full changelog**: $REPO_URL/commits/$tag"
  fi
  [ "$last" = "$want" ] || err "$f" "$n" "last line must be: $want"
}

run() {
  bad=0
  local files=("$@") f
  if [ ${#files[@]} -eq 0 ]; then
    for f in docs/releases/v*.md; do [ -f "$f" ] && files+=("$f"); done
    if [ ${#files[@]} -eq 0 ]; then echo "no release notes found under docs/releases"; return 1; fi
  fi
  for f in "${files[@]}"; do check_file "$f"; done
  [ "$bad" = 0 ] && echo "release notes: ${#files[@]} file(s) ok"
  return "$bad"
}

if [ "${1:-}" = "--self-test" ]; then
  tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
  d=$tmp/docs/releases; mkdir -p "$d"
  good_tail="**Full changelog**: $REPO_URL/compare/v1.0.0...v1.0.1"
  printf 'First.\n\n**Full changelog**: %s/commits/v1.0.0\n' "$REPO_URL" >"$d/v1.0.0.md"
  fail=0
  expect() { # $1 want(ok|fail) $2 label $3 body
    printf '%s\n' "$3" >"$d/v1.0.1.md"
    if out=$(run "$d/v1.0.0.md" "$d/v1.0.1.md"); then got=ok; else got=fail; fi
    if [ "$got" != "$1" ]; then echo "FAIL: $2: want $1, got $got"; echo "$out"; fail=1; fi
  }
  expect ok "well-formed" "$(printf 'Fixes a crash.\n\n### Fixed\n- `dot sync` no longer crashes. (#1)\n\n%s' "$good_tail")"
  expect ok "maintenance" "$(printf 'Maintenance release: no user-facing changes.\n\n%s' "$good_tail")"
  expect fail "heading on line 1" "$(printf '# v1.0.1\n\n%s' "$good_tail")"
  expect fail "unknown section" "$(printf 'X.\n\n### Changes\n- y (#1)\n\n%s' "$good_tail")"
  expect fail "section order" "$(printf 'X.\n\n### Fixed\n- a (#1)\n\n### New\n- b (#2)\n\n%s' "$good_tail")"
  expect fail "SHA" "$(printf 'X.\n\n### Fixed\n- a 0123456789abcdef0123456789abcdef01234567 (#1)\n\n%s' "$good_tail")"
  expect fail "conventional prefix" "$(printf 'X.\n\n### Fixed\n- fix(sync): a (#1)\n\n%s' "$good_tail")"
  expect fail "wrong compare link" "$(printf 'X.\n\n**Full changelog**: %s/compare/v0.9.0...v1.0.1' "$REPO_URL")"
  expect fail "missing link" "$(printf 'X.\n\n### Fixed\n- a (#1)')"
  # The oldest note links /commits/<tag>, and version order is numeric.
  printf 'First.\n\n**Full changelog**: %s/compare/v1.0.0...v1.0.1\n' "$REPO_URL" >"$d/v1.0.0.md"
  if run "$d/v1.0.0.md" >/dev/null; then echo "FAIL: oldest note accepted a compare link"; fail=1; fi
  printf 'First.\n\n**Full changelog**: %s/commits/v1.0.0\n' "$REPO_URL" >"$d/v1.0.0.md"
  printf 'X.\n\n**Full changelog**: %s/compare/v1.0.9...v1.0.10\n' "$REPO_URL" >"$d/v1.0.10.md"
  printf 'X.\n\n**Full changelog**: %s/compare/v1.0.1...v1.0.9\n' "$REPO_URL" >"$d/v1.0.9.md"
  if ! out=$(run "$d/v1.0.10.md"); then echo "FAIL: v1.0.10 should follow v1.0.9"; echo "$out"; fail=1; fi
  [ "$fail" = 0 ] && echo "check-release-notes self-test: ok"
  exit "$fail"
fi

run "$@"
