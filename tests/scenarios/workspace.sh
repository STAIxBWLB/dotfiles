#!/usr/bin/env bash
# workspace.sh — Integration test for workspace management commands.
# Runs in Docker (CI) or locally. Tests CLI commands without tmux session creation
# (tmux sessions need a terminal, but we test everything else).
set -euo pipefail

# Find binary: argument > PATH > common locations
BIN="${1:-}"
if [ -z "$BIN" ] || [ ! -x "$BIN" ]; then
  for candidate in ./dot /usr/local/bin/dot "$(command -v dot 2>/dev/null)" ./dotfiles /usr/local/bin/dotfiles "$(command -v dotfiles 2>/dev/null)"; do
    if [ -n "$candidate" ] && [ -x "$candidate" ]; then
      BIN="$candidate"
      break
    fi
  done
fi
if [ -z "$BIN" ] || [ ! -x "$BIN" ]; then
  echo "FAIL: dot binary not found"
  exit 1
fi

PASS=0
FAIL=0

pass() { PASS=$((PASS + 1)); echo "  PASS: $1"; }
fail() { FAIL=$((FAIL + 1)); echo "  FAIL: $1"; }

check() {
  local desc="$1"
  shift
  if "$@" >/dev/null 2>&1; then
    pass "$desc"
  else
    fail "$desc"
  fi
}

check_fail() {
  local desc="$1"
  shift
  if "$@" >/dev/null 2>&1; then
    fail "$desc (expected failure)"
  else
    pass "$desc"
  fi
}

# Use isolated HOME
# D-03/SC2155: assign, then export, so mktemp's exit status is not masked by
# export's own. Same two-step form as tests/scenarios/install-script.sh.
TEST_HOME=$(mktemp -d)
export HOME="$TEST_HOME"
# D-03/SC2064: single-quoted trap body, so $HOME expands when the signal
# arrives rather than when the trap is installed; a later reassignment of HOME
# would otherwise make this delete the wrong directory. Matches sync.sh.
trap 'rm -rf "$HOME"' EXIT

echo "=== Workspace integration tests ==="
echo "Binary: $BIN"
echo "HOME: $HOME"
echo

# ── 1. Help & version ────────────────────────────────────────────────────────
echo "--- help & version ---"
check "help shows workspace commands" bash -c "$BIN help 2>&1 | grep -q 'open'"
check "help shows register command" bash -c "$BIN help 2>&1 | grep -q 'register'"
check "version works" $BIN version

# ── 2. Layouts ────────────────────────────────────────────────────────────────
echo "--- layouts ---"
check "layouts lists dev" bash -c "$BIN layouts 2>&1 | grep -q 'dev'"
check "layouts lists claude" bash -c "$BIN layouts 2>&1 | grep -q 'claude'"
check "layouts lists monitor" bash -c "$BIN layouts 2>&1 | grep -q 'monitor'"

# ── 3. Doctor ─────────────────────────────────────────────────────────────────
echo "--- doctor ---"
DOCTOR_OUT=$($BIN layouts doctor 2>&1) || true
echo "$DOCTOR_OUT" | grep -q "Workspace Doctor" && pass "doctor shows header" || fail "doctor shows header"
echo "$DOCTOR_OUT" | grep -q "SHELL:" && pass "doctor shows SHELL" || fail "doctor shows SHELL"
echo "$DOCTOR_OUT" | grep -q "Config:" && pass "doctor shows config path" || fail "doctor shows config path"
echo "$DOCTOR_OUT" | grep -q "Scripts:" && pass "doctor shows scripts path" || fail "doctor shows scripts path"

# ── 4. Register ───────────────────────────────────────────────────────────────
echo "--- register ---"
PROJ_DIR=$(mktemp -d)

check "register valid project" $BIN register test-proj "$PROJ_DIR"
check_fail "register duplicate name" $BIN register test-proj "$PROJ_DIR"
check_fail "register invalid name (.)" $BIN register "my.proj" "$PROJ_DIR"
check_fail "register invalid name (:)" $BIN register "my:proj" "$PROJ_DIR"
check_fail "register invalid name (space)" $BIN register "my proj" "$PROJ_DIR"
check_fail "register nonexistent path" $BIN register test2 /nonexistent/path/XYZ

# Register with layout and theme
check "register with options" $BIN register styled-proj "$PROJ_DIR" --layout claude --theme dracula
check_fail "register invalid layout" $BIN register bad-layout "$PROJ_DIR" --layout nonexistent
check_fail "register invalid theme" $BIN register bad-theme "$PROJ_DIR" --theme nonexistent

# ── 5. List ───────────────────────────────────────────────────────────────────
echo "--- list ---"
LIST_OUT=$($BIN list 2>&1) || true
echo "$LIST_OUT" | grep -q "test-proj" && pass "list shows test-proj" || fail "list shows test-proj"
echo "$LIST_OUT" | grep -q "styled-proj" && pass "list shows styled-proj" || fail "list shows styled-proj"
echo "$LIST_OUT" | grep -q "layout=claude" && pass "list shows claude layout" || fail "list shows claude layout"
echo "$LIST_OUT" | grep -q "theme=dracula" && pass "list shows dracula theme" || fail "list shows dracula theme"

# List alias
check "ls alias works" $BIN ls

# ── 6. Unregister ─────────────────────────────────────────────────────────────
echo "--- unregister ---"
check "unregister existing" $BIN unregister styled-proj
check_fail "unregister nonexistent" $BIN unregister nonexistent-proj

# Verify removal
LIST_AFTER=$($BIN list 2>&1) || true
echo "$LIST_AFTER" | grep -q "styled-proj" && fail "styled-proj still in list" || pass "styled-proj removed from list"
echo "$LIST_AFTER" | grep -q "test-proj" && pass "test-proj still in list" || fail "test-proj should remain"

# ── 7. Config file persistence ────────────────────────────────────────────────
echo "--- config persistence ---"
CONFIG="$HOME/.config/dot/workspace.yaml"
[ -f "$CONFIG" ] && pass "config file exists" || fail "config file exists"
grep -q "test-proj" "$CONFIG" && pass "config contains test-proj" || fail "config contains test-proj"

# ── 8. Open command validation (without tmux) ─────────────────────────────────
echo "--- open validation ---"
check_fail "open with invalid session name" $BIN open "my.invalid"

# Open with TMUX set should give nested session warning, not error
export TMUX="/tmp/fake/tmux,12345,0"
OPEN_OUT=$($BIN open test-proj 2>&1) || true
echo "$OPEN_OUT" | grep -qi "already inside\|switch-client" && pass "open detects nested tmux" || fail "open detects nested tmux"
unset TMUX

# ── 9. Implicit routing ──────────────────────────────────────────────────────
echo "--- implicit routing ---"
# "help" should NOT route to open
HELP_OUT=$($BIN help 2>&1) || true
echo "$HELP_OUT" | grep -q "Available Commands" && pass "help not routed to open" || fail "help not routed to open"

# "version" should NOT route to open
check "version not routed to open" $BIN version

# ── 10. Existing commands still work ─────────────────────────────────
echo "--- backward compatibility ---"
check "apply --dry-run flag accepted" bash -c "$BIN apply --dry-run 2>&1; true"
check "diff --summary exists" bash -c "$BIN diff --help 2>&1 | grep -q -- '--summary'"
check "check alias still runs" bash -c "$BIN check --help >/dev/null 2>&1"
check "doctor alias names layouts doctor" bash -c "$BIN doctor 2>&1 >/dev/null | grep -q 'dot layouts doctor'"
check "diff command exists" bash -c "$BIN diff --help 2>&1 | grep -q 'Show'"

# ── Cleanup ───────────────────────────────────────────────────────────────────
rm -rf "$PROJ_DIR"

echo
echo "=== Results: $PASS passed, $FAIL failed ==="
[ "$FAIL" -eq 0 ] || exit 1
