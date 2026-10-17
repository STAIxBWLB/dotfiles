#!/usr/bin/env bash
# Decide whether this Test run needs the expensive jobs (one full validation per tree).
#
# run_full=false when
#   - every changed file is outside the test inputs (TEST_INPUTS, minus TEST_IGNORE), or
#   - a main push whose tree equals the head tree of a merged PR whose latest
#     Test run succeeded.
# Anything unknown (API failure, new branch, >=300 changed files) runs full.
#
# Env: GH_TOKEN GITHUB_REPOSITORY GITHUB_EVENT_NAME GITHUB_SHA GITHUB_OUTPUT
#      BEFORE (push) BASE_SHA HEAD_SHA (pull_request) WORKFLOW_FILE TEST_INPUTS TEST_IGNORE
# Self-check: scripts/ci-decision.sh --self-test
set -uo pipefail

# Prints the files from stdin that count as test inputs.
relevant_files() {
  grep -E "$TEST_INPUTS" | { if [ -n "${TEST_IGNORE:-}" ]; then grep -vE "$TEST_IGNORE"; else cat; fi; } || true
}

if [ "${1:-}" = "--self-test" ]; then
  TEST_INPUTS='^(cmd/|internal/|docs/|README\.md$)'
  TEST_IGNORE='^docs/releases/'
  fail=0
  check() { # $1 expected count, rest files
    want=$1; shift
    got=$(printf '%s\n' "$@" | relevant_files | grep -c . || true)
    if [ "$got" != "$want" ]; then echo "FAIL: [$*] relevant=$got want=$want"; fail=1; fi
  }
  check 0 docs/releases/v1.0.0.md
  check 0 AGENTS.md LICENSE
  check 1 docs/releases/v1.0.0.md internal/cli/root.go
  check 1 docs/CEILINGS.md
  check 1 README.md
  check 0 README.md.bak
  [ "$fail" = 0 ] && echo "ci-decision self-test: ok"
  exit "$fail"
fi

out() { echo "$1=$2" >> "$GITHUB_OUTPUT"; }
decide() {
  out run_full "$1"
  echo "Test decision: run_full=$1 ($2)"
  echo "### Test decision: run_full=$1" >> "${GITHUB_STEP_SUMMARY:-/dev/null}"
  echo "$2" >> "${GITHUB_STEP_SUMMARY:-/dev/null}"
  exit 0
}
api() { gh api -H "Accept: application/vnd.github+json" "$@"; }

case "$GITHUB_EVENT_NAME" in
  pull_request) range="$BASE_SHA...$HEAD_SHA" ;;
  push)
    if [ -z "${BEFORE:-}" ] || [ "$BEFORE" = "0000000000000000000000000000000000000000" ]; then
      decide true "new ref; no base to compare"
    fi
    range="$BEFORE...$GITHUB_SHA"
    ;;
  *) decide true "event $GITHUB_EVENT_NAME always runs full" ;;
esac

if ! cmp=$(api "repos/$GITHUB_REPOSITORY/compare/$range"); then
  decide true "could not compare $range"
fi
count=$(jq '.files | length' <<<"$cmp")
if [ "$count" -ge 300 ]; then
  decide true "$count changed files; the compare API caps the list"
fi
relevant=$(jq -r '.files[].filename' <<<"$cmp" | relevant_files)
if [ -z "$relevant" ]; then
  decide false "none of the $count changed files are test inputs"
fi

if [ "$GITHUB_EVENT_NAME" = "push" ]; then
  if pulls=$(api "repos/$GITHUB_REPOSITORY/commits/$GITHUB_SHA/pulls?per_page=100"); then
    pr=$(jq -c --arg sha "$GITHUB_SHA" '[.[] | select(.merged_at != null and .merge_commit_sha == $sha)] | sort_by(.number) | last // empty' <<<"$pulls")
    if [ -n "$pr" ]; then
      n=$(jq -r .number <<<"$pr"); head=$(jq -r .head.sha <<<"$pr")
      if push_tree=$(api "repos/$GITHUB_REPOSITORY/git/commits/$GITHUB_SHA" --jq .tree.sha) \
        && head_tree=$(api "repos/$GITHUB_REPOSITORY/git/commits/$head" --jq .tree.sha) \
        && runs=$(api "repos/$GITHUB_REPOSITORY/actions/workflows/$WORKFLOW_FILE/runs?event=pull_request&head_sha=$head&status=completed&per_page=20"); then
        last=$(jq -r '[.workflow_runs[]] | sort_by(.run_started_at) | last | .conclusion // ""' <<<"$runs")
        if [ "$push_tree" = "$head_tree" ] && [ "$last" = "success" ]; then
          decide false "PR #$n already validated the exact tree $push_tree"
        fi
      fi
    fi
  fi
fi
decide true "test inputs changed: $(head -n 5 <<<"$relevant" | tr '\n' ' ')"
