#!/usr/bin/env bash
# Run a CI command. When it fails on a network download (Homebrew, curl, DNS),
# mark the failure as infrastructure and retry once (#339). Any other failure
# is returned at once and never retried.
#   ci-net-retry.sh <command...>
#   ci-net-retry.sh --self-test
set -uo pipefail

net_failure='Failed during: .*brew|curl: \([0-9]+\)|Could not resolve host|Temporary failure in name resolution|Connection (timed out|reset|refused)|TLS handshake timeout|Error: Download failed'

if [[ ${1:-} == --self-test ]]; then
  self=${BASH_SOURCE[0]}
  fail() { echo "ci-net-retry self-test: $*" >&2; exit 1; }
  n=$(bash "$self" bash -c 'echo "curl: (6) Could not resolve host: x"; exit 7' | grep -c '^curl:')
  [[ $n == 2 ]] || fail "a network failure ran $n times, want 2"
  n=$(bash "$self" bash -c 'echo "  FAIL: assertion"; exit 1' | grep -c 'FAIL: assertion')
  [[ $n == 1 ]] || fail "a test failure ran $n times, want 1"
  bash "$self" bash -c 'exit 3' >/dev/null; rc=$?
  [[ $rc == 3 ]] || fail "exit status $rc, want 3"
  bash "$self" true >/dev/null || fail "a passing command failed"
  echo "ci-net-retry self-test: ok"
  exit 0
fi

log=$(mktemp "${TMPDIR:-/tmp}/ci-net-retry-XXXXXX")
trap 'rm -f "$log"' EXIT
status=0
for attempt in 1 2; do
  "$@" 2>&1 | tee "$log"
  status=${PIPESTATUS[0]}
  [[ $status -eq 0 ]] && exit 0
  grep -qE "$net_failure" "$log" || exit "$status"
  if [[ $attempt -eq 1 ]]; then
    echo "::warning title=Infrastructure (network)::a download failed; retrying once: $*"
  else
    echo "::error title=Infrastructure (network)::a download failed twice; a network failure, not a test failure: $*"
  fi
done
exit "$status"
