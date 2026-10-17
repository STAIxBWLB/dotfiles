#!/usr/bin/env bash
# assert.sh — Common test assertion functions for dotfiles
set -euo pipefail

PASS=0
FAIL=0
ERRORS=()

assert_file_exists() {
  local file="$1"
  local msg="${2:-File exists: $file}"
  if [[ -f "$file" ]]; then
    PASS=$((PASS + 1))
    echo "  ✓ $msg"
  else
    FAIL=$((FAIL + 1))
    ERRORS+=("FAIL: $msg — file not found: $file")
    echo "  ✗ $msg — file not found: $file"
  fi
}

assert_file_not_exists() {
  local file="$1"
  local msg="${2:-File does not exist: $file}"
  if [[ ! -f "$file" ]]; then
    PASS=$((PASS + 1))
    echo "  ✓ $msg"
  else
    FAIL=$((FAIL + 1))
    ERRORS+=("FAIL: $msg — file exists: $file")
    echo "  ✗ $msg — file exists: $file"
  fi
}

assert_file_contains() {
  local file="$1"
  local pattern="$2"
  local msg="${3:-File $file contains '$pattern'}"
  if grep -q "$pattern" "$file" 2>/dev/null; then
    PASS=$((PASS + 1))
    echo "  ✓ $msg"
  else
    FAIL=$((FAIL + 1))
    ERRORS+=("FAIL: $msg")
    echo "  ✗ $msg"
  fi
}

assert_dir_exists() {
  local dir="$1"
  local msg="${2:-Directory exists: $dir}"
  if [[ -d "$dir" ]]; then
    PASS=$((PASS + 1))
    echo "  ✓ $msg"
  else
    FAIL=$((FAIL + 1))
    ERRORS+=("FAIL: $msg — directory not found: $dir")
    echo "  ✗ $msg — directory not found: $dir"
  fi
}

assert_symlink() {
  local link="$1"
  local msg="${2:-Symlink exists: $link}"
  if [[ -L "$link" ]]; then
    PASS=$((PASS + 1))
    echo "  ✓ $msg"
  else
    FAIL=$((FAIL + 1))
    ERRORS+=("FAIL: $msg — not a symlink: $link")
    echo "  ✗ $msg — not a symlink: $link"
  fi
}

assert_command_exists() {
  local cmd="$1"
  local msg="${2:-Command exists: $cmd}"
  if command -v "$cmd" &>/dev/null; then
    PASS=$((PASS + 1))
    echo "  ✓ $msg"
  else
    FAIL=$((FAIL + 1))
    ERRORS+=("FAIL: $msg — command not found: $cmd")
    echo "  ✗ $msg — command not found: $cmd"
  fi
}

assert_exit_code() {
  local expected="$1"
  shift
  # Plain $* (not ${*@Q}): the message is display-only, and @Q is a bash 4.4+
  # transformation that breaks under macOS's system bash 3.2.
  local msg="$*"
  local actual
  actual=0
  "$@" >/dev/null 2>&1 || actual=$?
  if [[ "$actual" -eq "$expected" ]]; then
    PASS=$((PASS + 1))
    echo "  ✓ Exit code $expected: $msg"
  else
    FAIL=$((FAIL + 1))
    ERRORS+=("FAIL: Expected exit code $expected, got $actual: $msg")
    echo "  ✗ Expected exit code $expected, got $actual: $msg"
  fi
}

assert_no_changes() {
  local dir="$1"
  local snapshot_before="$2"
  local msg="${3:-No changes in $dir}"
  local snapshot_after
  snapshot_after=$(find "$dir" -type f -exec sha256sum {} \; 2>/dev/null | sort)
  if [[ "$snapshot_before" == "$snapshot_after" ]]; then
    PASS=$((PASS + 1))
    echo "  ✓ $msg"
  else
    FAIL=$((FAIL + 1))
    ERRORS+=("FAIL: $msg — files changed")
    echo "  ✗ $msg — files changed"
  fi
}

snapshot_dir() {
  local dir="$1"
  find "$dir" -type f -exec sha256sum {} \; 2>/dev/null | sort
}

report() {
  echo ""
  echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
  echo "Results: $PASS passed, $FAIL failed"
  if [[ ${#ERRORS[@]} -gt 0 ]]; then
    echo ""
    echo "Failures:"
    for err in "${ERRORS[@]}"; do
      echo "  $err"
    done
  fi
  echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
  [[ $FAIL -eq 0 ]]
}
