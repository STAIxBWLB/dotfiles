#!/usr/bin/env bash
# Scenario: coverage floor policy stays reachable and fail closed.
# shellcheck source=tests/assert.sh disable=SC1091
set -euo pipefail
source "$(dirname "$0")/../assert.sh"

pass() {
  PASS=$((PASS + 1))
  echo "  PASS: $1"
}

fail() {
  FAIL=$((FAIL + 1))
  ERRORS+=("FAIL: $1")
  echo "  FAIL: $1"
}

echo "=== Scenario: coverage-policy-gate ==="

REPO_ROOT=$(cd "$(dirname "$0")/../.." && pwd)
WORK_DIR=$(mktemp -d "${TMPDIR:-/tmp}/coverage-policy-gate-XXXXXX")
trap 'rm -rf "$WORK_DIR"' EXIT
CONFIG="$WORK_DIR/.testcoverage.yml"
WORKFLOW="$WORK_DIR/test.yaml"
GATE="$REPO_ROOT/scripts/check-coverage-floors.sh"

CONFIG_HASH=$(shasum -a 256 "$REPO_ROOT/.testcoverage.yml")
WORKFLOW_HASH=$(shasum -a 256 "$REPO_ROOT/.github/workflows/test.yaml")

run_gate() {
  GATE_OUT=$(bash "$GATE" "$CONFIG" "$WORKFLOW" 2>&1) && GATE_EXIT=0 || GATE_EXIT=$?
}

expect_green() {
  if [ "$GATE_EXIT" -eq 0 ]; then
    pass "$1"
  else
    fail "$1: expected exit 0, got $GATE_EXIT\n$GATE_OUT"
  fi
}

expect_red_with() {
  local what="$1"
  shift
  if [ "$GATE_EXIT" -ne 0 ]; then
    pass "$what exits non-zero"
  else
    fail "$what: expected non-zero exit, got 0\n$GATE_OUT"
  fi
  for needle in "$@"; do
    if printf '%s' "$GATE_OUT" | grep -Fq -- "$needle"; then
      pass "$what says $needle"
    else
      fail "$what: output did not mention $needle\n$GATE_OUT"
    fi
  done
}

copy_inputs() {
  cp "$REPO_ROOT/.testcoverage.yml" "$CONFIG"
  cp "$REPO_ROOT/.github/workflows/test.yaml" "$WORKFLOW"
}

PACKAGE_DEFAULT=$(awk '/^  package:/ { print $2; exit }' "$REPO_ROOT/.testcoverage.yml")

replace_package_default() {
  sed "s/^  package: $PACKAGE_DEFAULT$/  package: $1/" "$CONFIG" >"$CONFIG.next"
  mv "$CONFIG.next" "$CONFIG"
}

replace_first_override() {
  sed "s/^    threshold: 62$/    threshold: $1/" "$CONFIG" >"$CONFIG.next"
  mv "$CONFIG.next" "$CONFIG"
}

replace_first_override_with_nested_threshold() {
  awk '
    /^    threshold: 62$/ && !replaced {
      print "    policy:"
      print "      threshold: 1"
      replaced = 1
      next
    }
    { print }
  ' "$CONFIG" >"$CONFIG.next"
  mv "$CONFIG.next" "$CONFIG"
}

replace_first_override_with_inline_policy() {
  awk '
    /^    threshold: 62$/ && !replaced {
      print "    policy: { threshold: 1 }"
      replaced = 1
      next
    }
    { print }
  ' "$CONFIG" >"$CONFIG.next"
  mv "$CONFIG.next" "$CONFIG"
}

add_nested_threshold_beside_direct_floor() {
  awk '
    /^    threshold: 62$/ && !replaced {
      print "    policy:"
      print "      threshold: 1"
      print
      replaced = 1
      next
    }
    { print }
  ' "$CONFIG" >"$CONFIG.next"
  mv "$CONFIG.next" "$CONFIG"
}

replace_first_override_with_bad_indentation() {
  awk '
    /^    threshold: 62$/ && !replaced {
      print "   threshold: 62"
      replaced = 1
      next
    }
    { print }
  ' "$CONFIG" >"$CONFIG.next"
  mv "$CONFIG.next" "$CONFIG"
}

replace_first_override_with_tab_indentation() {
  awk '
    /^    threshold: 62$/ && !replaced {
      print "\tthreshold: 62"
      replaced = 1
      next
    }
    { print }
  ' "$CONFIG" >"$CONFIG.next"
  mv "$CONFIG.next" "$CONFIG"
}

echo "--- baseline ---"
copy_inputs
run_gate
expect_green "committed policy is green"
if printf '%s' "$GATE_OUT" | grep -Fq '34 package(s), 32 override(s), 2 exclusion(s)'; then
  pass "baseline reports the 34/32/2 partition"
else
  fail "baseline did not report the 34/32/2 partition\n$GATE_OUT"
fi

echo "--- workflow trigger mutation ---"
copy_inputs
sed 's/\\\.testcoverage\\\.yml\$|//' "$WORKFLOW" >"$WORKFLOW.next"
if cmp -s "$WORKFLOW" "$WORKFLOW.next"; then
  fail "workflow trigger mutation changed nothing; the decision TEST_INPUTS no longer names .testcoverage.yml"
fi
mv "$WORKFLOW.next" "$WORKFLOW"
run_gate
expect_red_with "missing floor-policy decision input" '.testcoverage.yml' 'TEST_INPUTS'

echo "--- zero override mutation ---"
copy_inputs
replace_first_override 0
run_gate
expect_red_with "zero override floor" '^internal/aisettings$' '0'

echo "--- package default numeric domain ---"
for value in 0 010 0100 0x64 0o100 +1 1.0 1e2 1_0 -1 101 nope; do
  copy_inputs
  replace_package_default "$value"
  run_gate
  expect_red_with "threshold.package $value" 'threshold.package' "$value"
done
copy_inputs
sed "/^  package: $PACKAGE_DEFAULT$/d" "$CONFIG" >"$CONFIG.next"
mv "$CONFIG.next" "$CONFIG"
run_gate
expect_red_with "missing threshold.package" 'threshold.package' '<missing>'
copy_inputs
awk '
  /^  package:/ && !added { print; print "  package: 2"; added = 1; next }
  { print }
' "$CONFIG" >"$CONFIG.next"
mv "$CONFIG.next" "$CONFIG"
run_gate
expect_red_with "duplicate threshold.package" 'threshold.package' "$PACKAGE_DEFAULT" '2'

echo "--- override numeric domain ---"
for value in 0 010 0100 0x64 0o100 +1 1.0 1e2 1_0 -1 101 nope; do
  copy_inputs
  replace_package_default 1
  replace_first_override "$value"
  run_gate
  expect_red_with "override floor $value" '^internal/aisettings$' "$value"
done
copy_inputs
replace_package_default 1
sed '/^    threshold: 62$/d' "$CONFIG" >"$CONFIG.next"
mv "$CONFIG.next" "$CONFIG"
run_gate
expect_red_with "missing override threshold" '^internal/aisettings$' '<missing>'
copy_inputs
replace_package_default 1
awk '
  /^    threshold: 62$/ && !added { print; print "    threshold: 63"; added = 1; next }
  { print }
' "$CONFIG" >"$CONFIG.next"
mv "$CONFIG.next" "$CONFIG"
run_gate
expect_red_with "duplicate override threshold" '^internal/aisettings$' '62' '63'

echo "--- inclusive override boundaries ---"
for value in 1 100; do
  copy_inputs
  replace_package_default 1
  replace_first_override "$value"
  run_gate
  expect_green "override floor $value is accepted"
done

echo "--- override structure ---"
copy_inputs
replace_first_override_with_nested_threshold
run_gate
expect_red_with "nested override threshold without a direct floor" '^internal/aisettings$' '<missing>' 'nested threshold:'

copy_inputs
replace_first_override_with_inline_policy
run_gate
expect_red_with "inline override policy without a direct floor" '^internal/aisettings$' '<missing>' 'unexpected direct child'

copy_inputs
add_nested_threshold_beside_direct_floor
run_gate
expect_red_with "nested override threshold beside a direct floor" '^internal/aisettings$' 'nested threshold:'

copy_inputs
replace_first_override_with_bad_indentation
run_gate
expect_red_with "misindented override threshold" '^internal/aisettings$' 'threshold indentation'

copy_inputs
replace_first_override_with_tab_indentation
run_gate
expect_red_with "tab-indented override threshold" '^internal/aisettings$' 'tabs are not allowed'

echo "--- preserved table invariants ---"
copy_inputs
awk '
  /^  - path: \^internal\/aisettings\$/ { skip = 1; next }
  skip && /^    threshold:/ { skip = 0; next }
  { print }
' "$CONFIG" >"$CONFIG.next"
mv "$CONFIG.next" "$CONFIG"
run_gate
expect_red_with "missing override entry" 'internal/aisettings has no floor'

copy_inputs
sed 's|\^internal/aisettings\$|^internal/not-a-package$|' "$CONFIG" >"$CONFIG.next"
mv "$CONFIG.next" "$CONFIG"
run_gate
expect_red_with "stale override path" '^internal/not-a-package$' 'matches no package'

copy_inputs
printf '%s\n' '    - ^internal/aisettings/ # scenario double listing' >>"$CONFIG"
run_gate
expect_red_with "double-listed package" 'internal/aisettings is in BOTH'

copy_inputs
awk '
  /^exclude:/ { exclude = 1 }
  exclude && /^[[:space:]]*#/ { next }
  { print }
' "$CONFIG" >"$CONFIG.next"
mv "$CONFIG.next" "$CONFIG"
run_gate
expect_red_with "reasonless exclusion" 'carries no comment'

copy_inputs
sed '/^    threshold: 91$/d' "$CONFIG" >"$CONFIG.next"
mv "$CONFIG.next" "$CONFIG"
run_gate
expect_red_with "missing preserved override threshold" '^internal/guard$' "has no 'threshold:' line"

copy_inputs
sed 's|\^internal/config\$|^internal/config|' "$CONFIG" >"$CONFIG.next"
mv "$CONFIG.next" "$CONFIG"
run_gate
expect_red_with "unanchored override" '^internal/config' 'not anchored at both ends'

if [ "$CONFIG_HASH" = "$(shasum -a 256 "$REPO_ROOT/.testcoverage.yml")" ] && \
  [ "$WORKFLOW_HASH" = "$(shasum -a 256 "$REPO_ROOT/.github/workflows/test.yaml")" ]; then
  pass "committed policy inputs remain unchanged"
else
  fail "scenario mutated a committed policy input"
fi

report
