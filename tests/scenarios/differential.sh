#!/usr/bin/env bash
# Scenario: differential — the binary built from the pinned milestone-start
# commit against the binary built from the current tree, over the read and
# --dry-run surfaces, diffing stdout, stderr, exit code and filesystem tree
# (GUARD-05)
set -euo pipefail
# shellcheck source=tests/assert.sh disable=SC1091
source "$(dirname "$0")/../assert.sh"

# Pinned so the mode field the tree snapshot records is host-independent.
# Without it a host running under umask 077 reports a mode difference that is
# the developer's shell talking, not the binary's behavior.
umask 022

START_SECONDS=$SECONDS
BUDGET_SECONDS=60
EXCERPT_LINES=40

# D-02: fixed for the whole milestone and moved only by hand, so accumulated
# drift cannot creep in one phase at a time. The workflow that builds the
# baseline carries the same SHA; this copy is here so a failure message can name
# what `dot-baseline` actually is.
BASELINE_COMMIT="db057e7ebbd21e0a7256dcd7c1b26ff3870a5478"

# Captured before any run is isolated, so CHECK_ISOLATION has something to
# assert against. Every comparison below must be blind to this path.
HOST_HOME="${HOME:-}"

# BUG-06 internal/exec/brew.go:293 - RefreshPath re-opens the PATH sandbox from
# an os.Stat, so read-only probes run real third-party binaries that write into
# HOME and reach the network. Once HOME is the sandbox (see ISOLATE), those
# writes land INSIDE the compared tree: `brew` refetches Homebrew's API and the
# two runs, seconds apart, record different payload hashes under
# Library/Caches/Homebrew. That is a host escape, not a behavior difference
# between the two binaries.
#
# Same runtime probe and same two prefixes as the Go half
# (internal/cli/dryrun_property_test.go:146), so the two surfaces agree on what
# "this host is contaminated" means. It disables the TREE field only; stdout,
# stderr and exit code stay unconditional.
#
# Deliberately NOT an expected-diff.tsv row. That file records intentional
# behavior changes, and a row for this would go red in CI where the escape never
# fires - the registry would assert both "this must differ" and "this must not
# differ" at once. This is the same call 01-03 made for the same defect.
#
# The prefix list is an env-defaulted variable rather than two literals so
# tests/scenarios/differential-registry-gate.sh can point the probe at a
# directory inside its own temp tree and drive the skipped-field arm
# deterministically on any host. It exists for that gate and nothing else: CI's
# linux job and every developer run leave it unset and get exactly the two
# paths below. Overriding it can only widen the comparison or narrow it to a
# directory the caller created — it can never forgive a difference.
DIFFERENTIAL_BREW_PREFIXES="${DIFFERENTIAL_BREW_PREFIXES:-/opt/homebrew/bin /home/linuxbrew/.linuxbrew/bin}"
BREW_PREFIXES=()
read -r -a BREW_PREFIXES <<< "$DIFFERENTIAL_BREW_PREFIXES"
TREE_COMPARISON=enabled
TREE_SKIP_REASON=""
for BREW_PREFIX in ${BREW_PREFIXES[@]+"${BREW_PREFIXES[@]}"}; do
  if [ -d "$BREW_PREFIX" ]; then
    TREE_COMPARISON=disabled
    TREE_SKIP_REASON="host homebrew at $BREW_PREFIX defeats the PATH sandbox: BUG-06 internal/exec/brew.go:293 - read-only probes run real third-party binaries that write into HOME and refetch over the network, so the tree field cannot carry a claim on this host. CI runs this scenario in a clean ubuntu:22.04 container where neither prefix exists and the tree field is compared unconditionally."
  fi
done

# Local helpers on top of assert.sh counters (no generic pass/fail there).
# Both append to ERRORS: a variant that only bumps the counter drops the failure
# detail from the terminal report.
pass() {
  PASS=$((PASS + 1))
  echo "  ✓ $1"
}
fail() {
  FAIL=$((FAIL + 1))
  ERRORS+=("FAIL: $1")
  echo "  ✗ $1"
}

# unchecked() is the third state (D-07): something this run could not compare at
# all, so it is neither a pass nor a failure. It carries its own counter and
# deliberately does NOT append to ERRORS - a row nobody looked at is not a
# failure detail, and listing it under "Failures:" is exactly the misreport
# BUG-17 was. The leading tilde is this file's existing "not asserted" marker
# (see the preflight banner and the budget block).
#
# tests/assert.sh report() is untouched: it counts PASS and FAIL only and is
# sourced by fourteen scenario files, so the third state is printed and counted
# here rather than pushed into shared code no other scenario needs.
UNCHECKED=0
unchecked() {
  UNCHECKED=$((UNCHECKED + 1))
  echo "  ~ $1"
}

# FIELD_COMPARED answers "did this run compare this field", reading the SAME
# variable the matrix loop branches on below, so the preflight banner and the
# end-of-run verdict can never disagree about what was compared. stdout, stderr
# and exit_code are unconditional; tree is the only field a preflight can
# disable.
FIELD_COMPARED() {
  if [ "$1" = tree ] && [ "$TREE_COMPARISON" != enabled ]; then
    return 1
  fi
  return 0
}

# ---------------------------------------------------------------------------
# The command matrix. Read and --dry-run surfaces only: this harness must never
# mutate anything outside the temp HOME it creates for each individual run.
# Ordered cheapest first so a failure surfaces early.
#
# `version` is deliberately IN the matrix rather than excluded. Its build
# metadata is normalized at the build step (identical -ldflags plus a pinned
# GOTOOLCHAIN, .github/workflows/test.yaml), so including it turns that
# normalization into something this harness proves on every run rather than
# something a comment claims. It is forbidden from the registry — see
# CHECK_REGISTRY_ROW below.
# ---------------------------------------------------------------------------
MATRIX=(
  "--help"
  "version"
  "status"
  "diff"
  "sync status --json"
  "peer status --json"
  "ai skills status --json"
  "apply --profile minimal --yes --dry-run"
  "apply --profile full --yes --dry-run"
  "apply --profile server --yes --dry-run"
)

# ---------------------------------------------------------------------------
# The expected-diff registry (D-04), loaded once into four parallel indexed
# arrays. bash 3.2 compatible: no associative array, no readarray.
# ---------------------------------------------------------------------------
REGISTRY="$(dirname "$0")/../expected-diff.tsv"
REG_COMMANDS=()
REG_FIELDS=()
REG_REASONS=()
REG_SEEN=()

# CHECK_REGISTRY_ROW rejects a row this file is not allowed to hold. Enforced
# here rather than left as a comment in the TSV, because a rule nobody runs is
# a rule that erodes: a `version` row would silently forgive exactly the
# build-metadata drift the normalization exists to remove.
CHECK_REGISTRY_ROW() {
  if [ "$1" = "version" ]; then
    fail "expected-diff.tsv registers the 'version' command (field: $2), which is forbidden: both binaries are built with identical -ldflags and a pinned GOTOOLCHAIN, so a version difference means the build normalization broke rather than that behavior changed. Fix the build, remove the row."
    return 1
  fi
  return 0
}

LOAD_REGISTRY() {
  local R_COMMAND R_FIELD R_REASON
  while IFS=$'\t' read -r R_COMMAND R_FIELD R_REASON || [ -n "${R_COMMAND:-}" ]; do
    case "$R_COMMAND" in
      '#'*) continue ;;
    esac
    [ -n "${R_COMMAND// /}" ] || continue
    CHECK_REGISTRY_ROW "$R_COMMAND" "$R_FIELD" || continue
    REG_COMMANDS+=("$R_COMMAND")
    REG_FIELDS+=("$R_FIELD")
    REG_REASONS+=("$R_REASON")
    REG_SEEN+=(0)
  done < "$REGISTRY"
}

# REGISTRY_MATCH prints the index of the row registering this command/field
# pair, or returns 1 when the difference is unregistered.
REGISTRY_MATCH() {
  local WANT_COMMAND="$1"
  local WANT_FIELD="$2"
  local I=0
  while [ "$I" -lt "${#REG_COMMANDS[@]}" ]; do
    if [ "${REG_COMMANDS[$I]}" = "$WANT_COMMAND" ] && [ "${REG_FIELDS[$I]}" = "$WANT_FIELD" ]; then
      printf '%s' "$I"
      return 0
    fi
    I=$((I + 1))
  done
  return 1
}

# ---------------------------------------------------------------------------
# SNAPSHOT_TREE records the WHOLE tree — relative path, entry type and mode for
# directories, symlinks and regular files alike, plus a content hash for regular
# files. The two tree-comparison helpers in tests/assert.sh are deliberately not
# used: they walk with `find -type f`, so they miss directories and symlinks,
# and they hash content but not modes. `dot` writes symlinks as its primary
# artifact, so a symlink-blind or mode-blind snapshot cannot support a claim
# about the whole tree. Both helpers also have zero callers in this repo, so
# reusing them would not be the free win it looks like.
#
# GNU findutils and coreutils are installed in tests/Dockerfile.ubuntu-22.04.
# ---------------------------------------------------------------------------
SNAPSHOT_TREE() {
  local DIR="$1"
  find "$DIR" -mindepth 1 -printf '%P\t%y\t%m\n' |
    while IFS=$'\t' read -r ENTRY_PATH ENTRY_TYPE ENTRY_MODE; do
      if [ "$ENTRY_TYPE" = "f" ]; then
        printf '%s\t%s\t%s\t%s\n' "$ENTRY_PATH" "$ENTRY_TYPE" "$ENTRY_MODE" \
          "$(sha256sum "$DIR/$ENTRY_PATH" | cut -d' ' -f1)"
      elif [ "$ENTRY_TYPE" = "l" ]; then
        # A symlink's payload is its target, exactly as a regular file's payload
        # is its bytes. Recording only path/type/mode would report a retargeted
        # symlink as identical - the blind spot that matters most here, since a
        # symlink IS what this tool primarily creates.
        printf '%s\t%s\t%s\t-> %s\n' "$ENTRY_PATH" "$ENTRY_TYPE" "$ENTRY_MODE" \
          "$(readlink "$DIR/$ENTRY_PATH")"
      else
        printf '%s\t%s\t%s\n' "$ENTRY_PATH" "$ENTRY_TYPE" "$ENTRY_MODE"
      fi
    done | LC_ALL=C sort
}

# NORMALIZE_OUTPUT erases exactly two things, both of which differ between two
# runs of the SAME binary and therefore carry no behavior signal at all. A
# normalizer that erases anything beyond that is erasing the signal this harness
# exists to carry.
#
#   1. This run's own temp HOME -> @HOME@. Two roots, not one: mktemp hands back
#      /tmp/... while the binary may report the resolved /private/tmp/... form.
#      The longer path is substituted first so the shorter one cannot
#      half-replace a nested match and leave a prefix behind.
#
#   2. The wall-clock value of slog's `time=` attribute -> @TIMESTAMP@.
#      slog.NewTextHandler stamps every record with time.Now() in RFC3339 and
#      the binary exposes no flag to suppress it, so `dot apply --dry-run` emits
#      three log lines per profile that never match across runs. Only the VALUE
#      is masked; the `time=` key, the level, the message and every other
#      attribute are compared byte for byte, so a refactor that drops, reorders
#      or reworders a log line is still caught. Two sed expressions rather than
#      one alternation because BSD sed has no \| in a basic regex.
#
#      This is deliberately fixed here and not in tests/expected-diff.tsv. A
#      registry row for it would be a row added to turn a red build green rather
#      than to record a decision, which is precisely what that file forbids.
NORMALIZE_OUTPUT() {
  local FILE="$1"
  local LOGICAL="$2"
  local PHYSICAL="$3"
  local STAMP='[0-9]\{4\}-[0-9]\{2\}-[0-9]\{2\}T[0-9]\{2\}:[0-9]\{2\}:[0-9]\{2\}[.0-9]*'
  if [ "${#PHYSICAL}" -gt "${#LOGICAL}" ]; then
    sed -e "s|$PHYSICAL|@HOME@|g" -e "s|$LOGICAL|@HOME@|g" "$FILE"
  else
    sed -e "s|$LOGICAL|@HOME@|g" -e "s|$PHYSICAL|@HOME@|g" "$FILE"
  fi |
    sed -e "s|time=${STAMP}[+-][0-9]\{2\}:[0-9]\{2\}|time=@TIMESTAMP@|g" \
      -e "s|time=${STAMP}Z|time=@TIMESTAMP@|g"
}

# ISOLATE runs a command with the environment stripped to nothing but PATH and a
# HOME pointing at the sandbox. `--home` alone is NOT enough and this is not a
# precaution: `dot status` ignores `--home` entirely for config and state
# resolution (`internal/cli/status_cmd.go:37` calls the bare `config.LoadState()`
# where `apply.go:39-62` threads the flag through `config.LoadStateForHome`), so
# a `--home`-only harness reads the invoking developer's real
# ~/.config/dotfiles — real name, email, hostname, and live sync timestamps that
# tick between the two binaries' runs. Recorded as BUG-07.
#
# `env -i` with a two-entry passthrough rather than a list of variables to
# override, because the list is the thing that rots: XDG_CONFIG_HOME
# (`internal/config/state.go:476`) outranks $HOME and defeats a HOME-only fix;
# XDG_CACHE_HOME, DOTFILES_HOME, DOTFILES_NAME, DOTFILES_EMAIL,
# DOTFILES_WORKSPACE_PATH, USER, GITHUB_USER and TZ all reach the resolution
# chain too, and the next one nobody has written yet would reach it silently.
# Naming what survives closes the class; naming what to clear closes today's
# members of it.
#
# Both binaries get the identical stripped environment, so this removes a
# nondeterminism source rather than introducing a difference.
ISOLATE() {
  local RUN_HOME="$1"
  shift
  env -i HOME="$RUN_HOME" PATH="$PATH" "$@"
}

# RUN_ONE runs one matrix entry with one binary against a HOME nobody else has
# touched, and leaves four artifacts behind: normalized stdout, normalized
# stderr, the exit code, and the tree the run left in that HOME. A fresh HOME
# per binary per command is what makes the matrix order-independent — no
# command's side effects can reach a later command's comparison.
RUN_ONE() {
  local BINARY="$1"
  local TAG="$2"
  local COMMAND="$3"
  local ARGS
  read -r -a ARGS <<< "$COMMAND"
  local RUN_HOME RUN_PHYSICAL CODE
  RUN_HOME=$(mktemp -d "$WORK_DIR/home-$TAG-XXXX")
  RUN_PHYSICAL=$(cd "$RUN_HOME" && pwd -P)
  CODE=0
  ISOLATE "$RUN_HOME" "$BINARY" "${ARGS[@]}" --home "$RUN_HOME" \
    >"$WORK_DIR/$TAG.stdout.raw" 2>"$WORK_DIR/$TAG.stderr.raw" || CODE=$?
  printf '%s\n' "$CODE" >"$WORK_DIR/$TAG.exit_code"
  NORMALIZE_OUTPUT "$WORK_DIR/$TAG.stdout.raw" "$RUN_HOME" "$RUN_PHYSICAL" >"$WORK_DIR/$TAG.stdout"
  NORMALIZE_OUTPUT "$WORK_DIR/$TAG.stderr.raw" "$RUN_HOME" "$RUN_PHYSICAL" >"$WORK_DIR/$TAG.stderr"
  SNAPSHOT_TREE "$RUN_HOME" >"$WORK_DIR/$TAG.tree"
  CHECK_ISOLATION "$COMMAND" "$TAG"
  rm -rf "$RUN_HOME"
}

# CHECK_ISOLATION fails when a run's output names the invoking user's real home.
# Without it the isolation is a claim in a comment; with it, a command that
# escapes the sandbox is reported as an escape rather than as a mysterious diff
# on whatever live value it leaked. This is the assertion that would have caught
# BUG-07 on the first run instead of on a lucky clock tick.
CHECK_ISOLATION() {
  local COMMAND="$1"
  local TAG="$2"
  # A HOST_HOME of / or empty would match everything; nothing to assert then.
  if [ -z "$HOST_HOME" ] || [ "$HOST_HOME" = "/" ]; then
    return 0
  fi
  if grep -qF "$HOST_HOME" "$WORK_DIR/$TAG.stdout" "$WORK_DIR/$TAG.stderr" 2>/dev/null; then
    fail "dot $COMMAND ($TAG) escaped its sandbox: output names the invoking user's home ($HOST_HOME). The comparison for this command is reading live host state, so its verdict means nothing."
  fi
}

# COMPARE_FIELD is the verdict: identical passes, a registered difference passes
# loudly with its reason so the CI log shows what was forgiven and why, and an
# unregistered difference fails with a bounded excerpt.
COMPARE_FIELD() {
  local COMMAND="$1"
  local FIELD="$2"
  local BASE_FILE="$3"
  local CUR_FILE="$4"
  local INDEX
  if diff -q "$BASE_FILE" "$CUR_FILE" >/dev/null 2>&1; then
    pass "dot $COMMAND — $FIELD identical"
    return 0
  fi
  if INDEX=$(REGISTRY_MATCH "$COMMAND" "$FIELD"); then
    REG_SEEN[INDEX]=1
    pass "dot $COMMAND — $FIELD differs, registered in expected-diff.tsv: ${REG_REASONS[$INDEX]}"
    return 0
  fi
  fail "dot $COMMAND — $FIELD differs between dot-baseline (pinned $BASELINE_COMMIT) and dot, and no row in tests/expected-diff.tsv registers it"
  echo "    --- $FIELD diff, first $EXCERPT_LINES lines (< baseline, > current) ---"
  { diff "$BASE_FILE" "$CUR_FILE" || true; } | head -n "$EXCERPT_LINES" | sed 's/^/    /' || true
  return 0
}

echo "=== Scenario: differential ==="

echo ""
echo "--- preflight ---"
PREFLIGHT_FAILED=0
for BINARY in dot dot-baseline; do
  if command -v "$BINARY" >/dev/null 2>&1; then
    pass "$BINARY is on PATH"
  else
    PREFLIGHT_FAILED=1
    fail "$BINARY is not on PATH — the differential compares two binaries. The linux job builds dot-baseline from $BASELINE_COMMIT on the runner and tests/Dockerfile.ubuntu-22.04 COPYs both into the image; one of those two steps is missing."
  fi
done

if [ -f "$REGISTRY" ]; then
  LOAD_REGISTRY
  pass "expected-diff.tsv loaded with ${#REG_COMMANDS[@]} registered change(s)"
else
  PREFLIGHT_FAILED=1
  fail "expected-diff.tsv not found at $REGISTRY — without the registry every difference would fail, including the ones somebody deliberately registered"
fi

if [ "$TREE_COMPARISON" = enabled ]; then
  pass "tree comparison enabled (no host package-manager prefix found)"
else
  echo "  ~ tree comparison SKIPPED on this host - $TREE_SKIP_REASON"
fi

if [ "$PREFLIGHT_FAILED" -ne 0 ]; then
  echo ""
  echo "Preflight failed — the comparison was not run."
  report || true
  exit 1
fi

WORK_DIR=$(mktemp -d /tmp/dotfiles-differential-XXXX)

for COMMAND in "${MATRIX[@]}"; do
  echo ""
  echo "--- dot $COMMAND ---"
  RUN_ONE dot-baseline baseline "$COMMAND"
  RUN_ONE dot current "$COMMAND"
  COMPARE_FIELD "$COMMAND" stdout "$WORK_DIR/baseline.stdout" "$WORK_DIR/current.stdout"
  COMPARE_FIELD "$COMMAND" stderr "$WORK_DIR/baseline.stderr" "$WORK_DIR/current.stderr"
  COMPARE_FIELD "$COMMAND" exit_code "$WORK_DIR/baseline.exit_code" "$WORK_DIR/current.exit_code"
  if [ "$TREE_COMPARISON" = enabled ]; then
    COMPARE_FIELD "$COMMAND" tree "$WORK_DIR/baseline.tree" "$WORK_DIR/current.tree"
  fi
done

echo ""
echo "--- dry-run applies still succeed on the current binary ---"
# The matrix above only proves the two binaries agree with each other. Two
# equally broken binaries agree perfectly, so assert the absolute contract too.
for PROFILE in minimal full server; do
  ASSERT_HOME=$(mktemp -d "$WORK_DIR/assert-XXXX")
  assert_exit_code 0 ISOLATE "$ASSERT_HOME" dot apply --profile "$PROFILE" --yes --dry-run --home "$ASSERT_HOME"
  rm -rf "$ASSERT_HOME"
done

echo ""
echo "--- expected-diff.tsv is not stale ---"
if [ "${#REG_COMMANDS[@]}" -eq 0 ]; then
  pass "expected-diff.tsv holds no data rows: the milestone is at full behavior preservation"
else
  I=0
  while [ "$I" -lt "${#REG_COMMANDS[@]}" ]; do
    # Order matters: the not-compared arm is first, because a row on a field the
    # matrix skipped can never have REG_SEEN set and would otherwise fall
    # through to the stale arm and be reported as reverted (BUG-17).
    if ! FIELD_COMPARED "${REG_FIELDS[$I]}"; then
      unchecked "registered row NOT CHECKED: ${REG_COMMANDS[$I]} / ${REG_FIELDS[$I]} — this run did not compare the ${REG_FIELDS[$I]} field, so it can say nothing about whether the change this row records still occurs. Do not remove the row on this run's evidence. $TREE_SKIP_REASON"
    elif [ "${REG_SEEN[$I]}" -eq 1 ]; then
      pass "registered change still occurs: ${REG_COMMANDS[$I]} / ${REG_FIELDS[$I]}"
    else
      # D-08: fail-closed for every field this run DID compare, so the registry
      # prunes itself instead of accumulating dead excuses.
      fail "expected-diff.tsv row matched no difference in this run, so the change it records appears reverted or superseded — remove the row: ${REG_COMMANDS[$I]} / ${REG_FIELDS[$I]} (${REG_REASONS[$I]})"
    fi
    I=$((I + 1))
  done
fi

echo ""
echo "--- budget ---"
ELAPSED=$((SECONDS - START_SECONDS))
echo "  comparison elapsed: ${ELAPSED}s (the baseline BUILD is outside this budget and is cached in CI)"
if [ "$TREE_COMPARISON" != enabled ]; then
  # BUG-06's probes refetch over the network into each fresh sandbox HOME, which
  # dominates the wall clock on a contaminated host. Asserting the budget here
  # would measure Homebrew, not the harness.
  echo "  ~ budget NOT asserted: the same host escape that disabled the tree field also inflates the clock"
elif [ "$ELAPSED" -le "$BUDGET_SECONDS" ]; then
  pass "comparison finished in ${ELAPSED}s, within the ${BUDGET_SECONDS}s budget"
else
  fail "comparison took ${ELAPSED}s, over the ${BUDGET_SECONDS}s budget"
fi

# A run that could not assert every registered row is not a full verification,
# and a green terminal that does not say so is how a developer concludes more
# than the run proved. Silent when the count is zero: an extra line on the
# authoritative run is noise.
if [ "$UNCHECKED" -ne 0 ]; then
  echo ""
  echo "  ~ $UNCHECKED registered row(s) could not be asserted because this run did not compare the field they name, so this run is NOT a full verification. CI's linux job compares every field in a clean ubuntu:22.04 container and is the authoritative run."
fi

# Cleanup
rm -rf "$WORK_DIR"

report
