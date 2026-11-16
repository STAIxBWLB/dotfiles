#!/usr/bin/env bash
# Scenario: dot noindex end to end (TEST-05, D-10)
#
# internal/noindex already sits at 91.2% unit coverage, so what is missing is
# not branch coverage: it is whether the BUILT binary, invoked the way a user
# invokes it, marks the right directories under the home it was pointed at and
# leaves the rest alone. One invocation walks every project tree under a home
# and writes files into directories the tool does not own, so the blast radius
# of a wrong predicate is every project the user has.
#
# Portability: only POSIX `find` features are used (`-name`), unlike
# tests/scenarios/secrets.sh which needs GNU `find -printf`. That keeps this
# scenario runnable against a locally built binary as well as in the container.
set -euo pipefail
# shellcheck source=tests/assert.sh disable=SC1091
source "$(dirname "$0")/../assert.sh"

# assert.sh carries no generic pass/fail/skip. pass and fail both feed the
# terminal report; skip is a third state for a claim this host cannot make, and
# it is printed separately so it can never be read as a pass.
pass() {
  PASS=$((PASS + 1))
  echo "  ✓ $1"
}
fail() {
  FAIL=$((FAIL + 1))
  ERRORS+=("FAIL: $1")
  echo "  ✗ $1"
}
SKIPPED=()
skip() {
  SKIPPED+=("$1")
  echo "  ~ SKIPPED: $1"
}

# ABORT records a fixture failure the rest of the run cannot proceed past, and
# prints the report at the point of failure rather than after a cascade of
# assertions that were all about a half-built fixture.
ABORT() {
  fail "$1"
  print_skipped
  report || true
  exit 1
}

print_skipped() {
  if [ ${#SKIPPED[@]} -gt 0 ]; then
    echo ""
    echo "Skipped (neither passed nor failed):"
    for s in "${SKIPPED[@]}"; do
      echo "  ~ $s"
    done
  fi
}

# MARKER is noindex.Marker (internal/noindex/noindex.go:28). The literal is
# here rather than read from the source because the test image copies only
# tests/ — there is no Go tree at runtime in the container. When the source IS
# present (a local run from a checkout) the two are compared below, so a
# renamed constant fails loudly instead of quietly making every marker
# assertion in this file vacuous.
MARKER=".metadata_never_index"

# FIXTURE builds one sandbox home.
#
# Every root name below is taken from a list in internal/noindex/noindex.go and
# nowhere else. A fixture whose roots are not in those lists produces a sweep
# that finds nothing, and a scenario that passes for the wrong reason:
#
#   workspace   walkRootNames  (noindex.go:30-41) -- walked, every match inside marked
#   Sites       walkRootNames  -- deliberately NOT created, see group 1
#   .npm        cacheRootNames (noindex.go:45-58) -- one marker at the top, not walked
#
# and the directories inside the project tree come from clean.DefaultPatterns
# (internal/clean/clean.go:62-84), split by noindex's own keepIndexed
# (noindex.go:87-90):
#
#   node_modules, .venv, dist, target   match dirPatterns  -> marked
#   build, out                          keepIndexed        -> NOT marked
#   src                                 not a pattern      -> NOT marked
FIXTURE() {
  local H="$1"
  mkdir -p \
    "$H/workspace/proj/node_modules/dep" \
    "$H/workspace/proj/.venv" \
    "$H/workspace/proj/dist" \
    "$H/workspace/proj/target" \
    "$H/workspace/proj/build" \
    "$H/workspace/proj/out" \
    "$H/workspace/proj/src" \
    "$H/.npm/inner/node_modules"
}

# EXPECTED_MARKED is the count of the five directories a full sweep of a fresh
# FIXTURE must mark: the .npm cache root itself, plus node_modules, .venv, dist
# and target inside the project tree. It is an exact count, not a floor:
# a floor would pass a sweep that also marked build/ and out/.
EXPECTED_MARKED=5

# MARKED_COUNT parses the count out of the success line rather than grepping
# for the number. The line embeds no paths today, but a `grep -q 5` against
# output that ever does would match a mktemp suffix and pass vacuously.
#
# Two separate BRE expressions, one per verb: `\|` alternation is a GNU sed
# extension and this scenario has to parse the same line under BSD sed.
MARKED_COUNT() {
  sed -n \
    -e 's/^marked \([0-9][0-9]*\) directories.*/\1/p' \
    -e 's/^[[:space:]]*~[[:space:]]*would mark \([0-9][0-9]*\) directories.*/\1/p' \
    "$1" | head -1
}

# PRESENT_COUNT parses the already-marked count, which appears in both the
# "nothing to do (N already marked)" line and the trailing "(N already marked)"
# of a normal run.
PRESENT_COUNT() {
  sed -n 's/.*(\([0-9][0-9]*\) already marked).*/\1/p' "$1" | head -1
}

# MARKERS_UNDER lists every marker in a tree, one relative path per line.
MARKERS_UNDER() {
  find "$1" -name "$MARKER" 2>/dev/null | sed "s|^$1/||" | LC_ALL=C sort
}

echo "=== Scenario: noindex ==="

WORK_DIR=$(mktemp -d /tmp/dotfiles-noindex-XXXX)
# Cleanup on the failure path too: the container outlives this script and each
# home below is a multi-level tree.
trap 'rm -rf "$WORK_DIR"' EXIT

MARKER_SRC="$(dirname "$0")/../../internal/noindex/noindex.go"
if [ -f "$MARKER_SRC" ]; then
  SRC_MARKER=$(sed -n 's/^const Marker = "\(.*\)".*$/\1/p' "$MARKER_SRC" | head -1)
  if [ "$SRC_MARKER" = "$MARKER" ]; then
    pass "the marker name this scenario asserts on matches noindex.Marker in the source"
  else
    fail "noindex.Marker is '$SRC_MARKER' but this scenario asserts on '$MARKER'; every marker assertion below would be vacuous"
  fi
else
  skip "noindex.Marker could not be cross-checked: no Go source tree at $MARKER_SRC (expected in the test image, which copies only tests/)"
fi

echo ""
echo "--- group 1: a full sweep marks the regenerable directories under a project root ---"

SWEPT="$WORK_DIR/home-swept"
FIXTURE "$SWEPT"

# A default walk root that is absent must be skipped rather than erroring, and
# the claim is only worth making if the fixture really lacks one.
if [ -e "$SWEPT/Sites" ]; then
  ABORT "the fixture created $SWEPT/Sites; the absent-walk-root claim below needs it to be missing"
fi

SWEEP_LOG="$WORK_DIR/sweep.log"
SWEEP_RC=0
dot noindex --home "$SWEPT" > "$SWEEP_LOG" 2>&1 || SWEEP_RC=$?
cat "$SWEEP_LOG"
if [ "$SWEEP_RC" -eq 0 ]; then
  pass "dot noindex --home exited 0 with ~/Sites (a default walk root) absent, so a missing root is skipped rather than failing the sweep"
else
  ABORT "dot noindex --home exited $SWEEP_RC; every assertion below would be about a sweep that did not run"
fi

SWEEP_MARKED=$(MARKED_COUNT "$SWEEP_LOG")
if [ -z "$SWEEP_MARKED" ]; then
  fail "the sweep printed no 'marked N directories' line, so it reported nothing to do: $(head -1 "$SWEEP_LOG")"
elif [ "$SWEEP_MARKED" -eq "$EXPECTED_MARKED" ]; then
  pass "the sweep reported marking exactly $EXPECTED_MARKED directories, so it neither marked nothing nor over-marked"
else
  fail "the sweep reported marking $SWEEP_MARKED directories, expected $EXPECTED_MARKED; markers found: $(MARKERS_UNDER "$SWEPT" | tr '\n' ' ')"
fi

assert_file_exists "$SWEPT/workspace/proj/node_modules/$MARKER" \
  "a regenerable directory inside a walked project root carries the marker"
assert_file_exists "$SWEPT/workspace/proj/.venv/$MARKER" \
  "a second regenerable directory in the same project carries the marker"

# A cache root is stamped at the top INSTEAD of being walked. The pair is what
# says so: the top carries a marker and a matching directory buried inside it
# does not.
assert_file_exists "$SWEPT/.npm/$MARKER" \
  "the .npm cache root carries one marker at its top"
assert_file_not_exists "$SWEPT/.npm/inner/node_modules/$MARKER" \
  "a node_modules buried inside the cache root has no marker of its own, so the cache root was stamped rather than walked"

echo ""
echo "--- group 2: --dry-run reports what it would mark and writes nothing ---"

# A second home rather than a reordering of the groups. The dry-run claim is an
# ABSENCE claim, and asserting absence against the tree group 1 has already
# marked would be an assertion about the wrong thing.
DRY="$WORK_DIR/home-dryrun"
FIXTURE "$DRY"
if [ -n "$(MARKERS_UNDER "$DRY")" ]; then
  ABORT "the dry-run fixture at $DRY already carries markers, so 'wrote nothing' could not be told from 'was already marked'"
fi
pass "the dry-run fixture carries no marker before the run, so the absence claim below is about this run"

DRY_LOG="$WORK_DIR/dryrun.log"
DRY_RC=0
dot noindex --home "$DRY" --dry-run > "$DRY_LOG" 2>&1 || DRY_RC=$?
cat "$DRY_LOG"
if [ "$DRY_RC" -eq 0 ]; then
  pass "dot noindex --home --dry-run exited 0"
else
  fail "dot noindex --home --dry-run exited $DRY_RC"
fi

assert_file_contains "$DRY_LOG" "would mark" \
  "the dry run reports in the would-mark verb rather than claiming it marked anything"

DRY_MARKED=$(MARKED_COUNT "$DRY_LOG")
if [ -z "$DRY_MARKED" ]; then
  fail "the dry run printed no count line, so it reported nothing to do: $(head -1 "$DRY_LOG")"
elif [ "$DRY_MARKED" -eq "$EXPECTED_MARKED" ]; then
  pass "the dry run reported the same $EXPECTED_MARKED directories a real sweep marks, so the report is of a sweep that would do something"
else
  fail "the dry run reported $DRY_MARKED directories, expected $EXPECTED_MARKED"
fi

DRY_AFTER=$(MARKERS_UNDER "$DRY")
if [ -z "$DRY_AFTER" ]; then
  pass "no marker exists anywhere under the fixture after the dry run"
else
  fail "--dry-run wrote markers into the fixture: $(echo "$DRY_AFTER" | tr '\n' ' ')"
fi

echo ""
echo "--- group 3: build/ and out/ survive the sweep that marked their siblings ---"

# keepIndexed (internal/noindex/noindex.go:87-90) is the reason these two
# directories are exceptions to the command's own rule: build/ and out/ are
# where finished deliverables land, and a deck you cannot find by name costs
# more than the indexing it saves. Nothing outside a unit test has ever checked
# it. dist/ and target/ are the other half of that same comment -- regenerable
# from indexed sources, so they stay excluded from Spotlight.
assert_file_exists "$SWEPT/workspace/proj/dist/$MARKER" \
  "dist/ IS marked: keepIndexed covers build and out, not every risky build output"
assert_file_exists "$SWEPT/workspace/proj/target/$MARKER" \
  "target/ IS marked, for the same reason as dist/"
assert_file_not_exists "$SWEPT/workspace/proj/build/$MARKER" \
  "build/ is NOT marked, so a finished deliverable there stays findable"
assert_file_not_exists "$SWEPT/workspace/proj/out/$MARKER" \
  "out/ is NOT marked, for the same reason as build/"
assert_file_not_exists "$SWEPT/workspace/proj/src/$MARKER" \
  "src/, which matches no pattern at all, was left alone"

# The pair, asserted together in one condition. Each absence assertion above
# would pass on a sweep that marked nothing whatsoever, which is the vacuity
# this group is most exposed to.
if [ -f "$SWEPT/workspace/proj/dist/$MARKER" ] &&
  [ ! -f "$SWEPT/workspace/proj/build/$MARKER" ] &&
  [ ! -f "$SWEPT/workspace/proj/out/$MARKER" ]; then
  pass "the carve-out holds as a pair: a regenerable sibling IS marked in the same directory where build/ and out/ are NOT, so a sweep that did nothing cannot pass as a carve-out"
else
  fail "the carve-out pair does not hold; markers under the project: $(MARKERS_UNDER "$SWEPT/workspace/proj" | tr '\n' ' ')"
fi

echo ""
echo "--- group 4: a second sweep reports the directories as already marked ---"

BEFORE_SECOND=$(MARKERS_UNDER "$SWEPT")
SECOND_LOG="$WORK_DIR/second.log"
SECOND_RC=0
dot noindex --home "$SWEPT" > "$SECOND_LOG" 2>&1 || SECOND_RC=$?
cat "$SECOND_LOG"
if [ "$SECOND_RC" -eq 0 ]; then
  pass "the second sweep over an already-marked home exited 0"
else
  fail "the second sweep exited $SECOND_RC"
fi

SECOND_MARKED=$(MARKED_COUNT "$SECOND_LOG")
if [ -z "$SECOND_MARKED" ]; then
  pass "the second sweep printed no marked-count line at all, so it marked nothing new"
else
  fail "the second sweep marked $SECOND_MARKED further directories over a home that was already fully marked"
fi

SECOND_PRESENT=$(PRESENT_COUNT "$SECOND_LOG")
if [ -z "$SECOND_PRESENT" ]; then
  fail "the second sweep reported no already-marked count: $(head -1 "$SECOND_LOG")"
elif [ "$SECOND_PRESENT" -eq "$EXPECTED_MARKED" ]; then
  pass "the second sweep reported all $EXPECTED_MARKED directories as already marked, which is non-zero and therefore not an empty sweep reporting success"
else
  fail "the second sweep reported $SECOND_PRESENT already marked, expected $EXPECTED_MARKED"
fi

if [ "$BEFORE_SECOND" = "$(MARKERS_UNDER "$SWEPT")" ]; then
  pass "the set of markers under the home is identical before and after the second sweep"
else
  fail "the second sweep changed the set of markers under the home"
fi

echo ""
echo "--- group 5: an explicit path argument, present and absent ---"

EXPLICIT="$WORK_DIR/home-explicit"
FIXTURE "$EXPLICIT"

# EXPECTED_EXPLICIT is EXPECTED_MARKED minus the .npm cache root: a path
# argument REPLACES the default roots rather than adding to them, so the four
# matches inside the named project tree are all a sweep of it can find.
EXPECTED_EXPLICIT=$((EXPECTED_MARKED - 1))

# Absolute on purpose. absPath resolves a relative argument against the process
# working directory, which in the container is the test user's home and not the
# sandbox.
EXPL_LOG="$WORK_DIR/explicit.log"
EXPL_RC=0
dot noindex --home "$EXPLICIT" "$EXPLICIT/workspace/proj" > "$EXPL_LOG" 2>&1 || EXPL_RC=$?
cat "$EXPL_LOG"
if [ "$EXPL_RC" -eq 0 ]; then
  pass "dot noindex --home with an absolute path argument exited 0"
else
  fail "dot noindex --home with an absolute path argument exited $EXPL_RC"
fi

EXPL_MARKED=$(MARKED_COUNT "$EXPL_LOG")
if [ -z "$EXPL_MARKED" ]; then
  fail "the explicit-path sweep printed no count line: $(head -1 "$EXPL_LOG")"
elif [ "$EXPL_MARKED" -eq "$EXPECTED_EXPLICIT" ]; then
  pass "the explicit-path sweep marked exactly the $EXPECTED_EXPLICIT matches inside the named tree"
else
  fail "the explicit-path sweep marked $EXPL_MARKED directories, expected $EXPECTED_EXPLICIT; markers: $(MARKERS_UNDER "$EXPLICIT" | tr '\n' ' ')"
fi

assert_file_exists "$EXPLICIT/workspace/proj/node_modules/$MARKER" \
  "the sweep marked inside the tree it was pointed at"
assert_file_not_exists "$EXPLICIT/.npm/$MARKER" \
  "the default cache root is untouched, so a path argument replaces the default roots rather than adding to them"

MISSING="$WORK_DIR/home-missing"
FIXTURE "$MISSING"
MISS_LOG="$WORK_DIR/missing.log"
MISS_RC=0
# Two arguments, the first of which exists: the refusal is supposed to come
# before ANY sweep, so a partial sweep of the valid argument would be a defect
# a single bad argument could not surface.
dot noindex --home "$MISSING" "$MISSING/workspace/proj" "$MISSING/not-here" > "$MISS_LOG" 2>&1 || MISS_RC=$?
cat "$MISS_LOG"
if [ "$MISS_RC" -ne 0 ]; then
  pass "a path argument that does not exist was refused (exit $MISS_RC)"
else
  fail "a path argument that does not exist was accepted; the command exited 0"
fi
assert_file_contains "$MISS_LOG" "$MISSING/not-here" \
  "the refusal names the path that does not exist"

MISS_AFTER=$(MARKERS_UNDER "$MISSING")
if [ -z "$MISS_AFTER" ]; then
  pass "the refused run marked nothing at all, not even under the argument that did exist, so it refused before sweeping rather than after a partial pass"
else
  fail "the refused run left markers behind: $(echo "$MISS_AFTER" | tr '\n' ' ')"
fi

echo ""
echo "--- group 6: noindex setup is refused where launchd does not exist ---"

SETUP_HOME="$WORK_DIR/home-setup"
mkdir -p "$SETUP_HOME"
# noindexLabel (internal/cli/noindex_cmd.go:19) and noindexPlistPath (:243).
SETUP_PLIST="$SETUP_HOME/Library/LaunchAgents/com.dotfiles.noindex.plist"
if [ "$(uname -s)" = "Linux" ]; then
  SETUP_LOG="$WORK_DIR/setup.log"
  SETUP_RC=0
  dot noindex setup --home "$SETUP_HOME" > "$SETUP_LOG" 2>&1 || SETUP_RC=$?
  cat "$SETUP_LOG"
  if [ "$SETUP_RC" -ne 0 ]; then
    pass "dot noindex setup was refused on this non-darwin host (exit $SETUP_RC)"
  else
    fail "dot noindex setup exited 0 on a host with no launchd"
  fi
  assert_file_contains "$SETUP_LOG" "launchd" \
    "the refusal names launchd as the reason rather than failing opaquely"
  # The refusal is the first statement in the RunE body after the flags are
  # read (internal/cli/noindex_cmd.go:146-148), ahead of resolveGuardDotPath
  # and noindexPlistPath, so the plist path is never even built.
  assert_file_not_exists "$SETUP_PLIST" \
    "the refused setup wrote no plist under the target home"
else
  skip "the setup refusal is a non-darwin claim and this host is $(uname -s), where the command installs the LaunchAgent instead of refusing; CI's linux job is where this group runs"
fi

print_skipped

report
