# Peer Git Status and Realign

Status: Phase A implementation on `feat/peer-git-realign`; handover/takeover (Phase C) and content-based worktree exclusion (Phase B) pending.
Date: 2026-09-28
Issue: https://github.com/STAIxBWLB/dotfiles/issues/147

Superseded in part by #177, #178 and #179 (PR #189): ties are broken by the children's content, then the parent's gitlink, then the newest candidate; `--rescue` moves a diverged or branch-mismatch repo sideways onto a rescue-kept HEAD; `--fetch` fetches a missing gitlink commit; a stale `.gitmodules` is restored in the worktree. The decisions below that say otherwise describe Phase A.

## Problem and evidence

`dot peer` copies the working trees of the workspace root and every submodule
between two Macs but strips `.git` at every depth. Files travel; HEAD, index
and refs stay behind. Nothing re-derives HEAD and index when files arrive on
the Mac that did not make the commits.

Measured on the standby Mac on 2026-09-27 (verified in the issue): 15 of 24
top-level submodules had HEAD behind the gitlink recorded by the workspace
root, with `git status` showing up to 398 entries per repo. For all 15 the
best-matching commit was a strict descendant of HEAD. For 10 the worktree was
identical to the gitlink's tree; the other 5 differed only by untracked files
or the coordinator's uncommitted work. An index-only `reset --mixed` fixed
every one with zero file changes. Committing on the receiving Mac without
realigning first fails reproducibly: the push is rejected as non-fast-forward,
and `pull --rebase` then refuses with "unstaged changes", because the other
Mac's commits show up there as local edits.

## Decisions

- Git state never moves machine to machine. `dot peer` moves files; history
  moves only through `origin`. This command never fetches; the caller fetches
  beforehand when fresh upstream state is wanted.
- After a switch, the new active Mac realigns rather than pulls: HEAD and
  index move to the descendant commit that its files already match. The
  worktree is never written by git.
- Realign is fast-forward only, compare-and-swap against the expected old
  value. A ref never moves backward or sideways.
- Linked worktrees are listed but never realigned.

Rejected: rsync of `.git` (machine-local config, index stat data, locks, live
writers); machine-to-machine Git transport; automating `reset --mixed
origin/main` (drops local-only commits, picks the wrong base for repos ahead
of their gitlink).

## Command surface

- `dot peer git status [--json] [<repo>...]` classifies every discovered repo
  and prints a report. The JSON document is `schemaVersion: 1`, additive only.
- `dot peer git realign [--apply] [<repo>...]` runs the same classification
  and previews the moves. Only `--apply` mutates. The global `--dry-run` flag
  always wins over `--apply`: under it nothing changes in `.git` or in either
  peer store.

Optional `<repo>...` arguments are workspace-relative paths (`.` is the root)
and restrict the run; an argument that matches no discovered repo is an error
before any mutation.

## Discovery

Repo discovery covers the workspace root plus every submodule, recursively,
parent first. Children are found by reading `git ls-tree -r HEAD` of the
parent after the parent has been processed, so a child's candidate gitlink is
read from its parent's realigned HEAD. A directory whose `.git` file points at
a gitdir containing `commondir` is a linked worktree: it is listed in the
report and never realigned, and discovery does not recurse into it. A gitlink
path with no checked-out repo is reported as skipped.

## Candidates and content test

Candidates for a repo are the gitlink recorded by the parent (the root has
none) and the first-parent chain from HEAD to the branch upstream
(`git rev-list --first-parent HEAD..@{upstream}`). Each candidate must be a
strict descendant of HEAD, verified with `git merge-base --is-ancestor`.

The content test for a candidate C writes no objects and holds no lock on the
real index:

1. Copy the repo's index to a temp file and point `GIT_INDEX_FILE` at it.
2. `git read-tree -m C`. A one-tree merge refuses to replace entries the
   worktree has modified ("not uptodate"), which is exactly the realign case,
   so a refusal retries from an empty (missing) index and lets the refresh
   decide by content. The copy only preserves stat info for speed.
3. `git update-index -q --refresh` (its non-zero "needs update" exit is the
   expected signal, not an error).
4. `git diff-files --name-only` — the line count is the number of tracked
   differences. Untracked files never appear and never block.

The best candidate is the strict descendant of HEAD with the fewest tracked
differences, ties broken in candidate order (gitlink first, then the chain
nearest HEAD first). A repo whose worktree already matches HEAD is `aligned`,
unless a strict descendant carries the same tree (empty commits, a
net-unchanged sequence, or the parent gitlink): then the repo is
`realignable` to that descendant, because HEAD would otherwise stay behind
forever. A repo with a candidate whose diff count is at most HEAD's is
`realignable`
(a tie is accepted so a parent whose only drift is a child's gitlink still
moves and the child's own realign then reads the parent's new HEAD). Anything
else is `no-match`. All read commands run with `--no-optional-locks`.

## Skip and unresolvable conditions

A repo is skipped and reported with a reason, never touched, when any of these
hold: `index.lock` present, an operation in progress (`MERGE_HEAD`,
`CHERRY_PICK_HEAD`, `REVERT_HEAD`, `REBASE_HEAD`, `rebase-merge/`,
`rebase-apply/`), unmerged index entries, staged changes, or no HEAD yet. A
missing object — a parent gitlink naming a commit the repo does not have, or
an unenumerable upstream chain — marks that repo `unresolvable`; the run
continues with the remaining repos. `git` is resolved to an absolute path once
per run, because launchd and non-interactive shells do not share the user's
PATH.

## Realign protocol

Realign uses git's lockfile protocol, per repo, in parent-first order:

1. Create `.git/index.lock` with `O_EXCL`.
2. Build the temp index for the best candidate (the content test above) and
   write it into the lock.
3. `git update-ref -m "dot peer realign" HEAD <new> <old>` — compare-and-swap;
   a failure removes the lock and leaves HEAD and the index untouched.
4. Rename the lock over `index`. If the rename fails after HEAD moved, HEAD
   is rolled back with `update-ref -m "dot peer realign rollback" HEAD <old>`
   and the lock is removed; the repo is reported `unresolvable`, never
   `realigned`, so a partial realign cannot be misreported.

The worktree is never written by git; uncommitted modifications survive as
modifications against the new HEAD. Staged selections do not survive, which is
why staged repos are skipped instead. `<old>` is recorded in the report and
printed by the command; undo is `git reset --mixed -q <old>`. The reflog entry
written by `update-ref -m` preserves the same record inside the repo.

## Constraints from earlier fixes

| Earlier incident or fix | Constraint honored here |
|---|---|
| Manual `reset --mixed origin/main` recoveries dropped standby-only commits | Fast-forward only, compare-and-swap against the expected old value. |
| Identifying the worktree's commit with a temp index and `add -A` wrote loose blobs for every WIP file | Classification writes no objects: temp index via `read-tree -m`, `update-index --refresh`, `diff-files`; reads use `--no-optional-locks`. |
| Stale `index.lock` (09-24) | Skip and report a repo with a lock, an operation in progress, unmerged entries or staged changes. |
| A history rewrite (09-03) left gitlinks to vanished commits | A missing object marks that repo `unresolvable`; the run continues. |
| `--dry-run` mutated state (#99, #103) | Under `--dry-run` nothing changes in `.git` or in either peer store; `realign` previews unless `--apply`. |
| launchd PATH lacks `/usr/sbin`; non-interactive ssh picked `/usr/bin/rsync` | `git` is resolved to an absolute path once per run. |

## Accepted limits

- A repo whose files match no descendant commit is reported `no-match` and
  left alone; recovering it is a human decision.
- Staged selections do not survive a realign; realign skips repos with staged
  changes.
- Linked worktrees are reported, not realigned. Their exclusion from peer
  transfer is Phase B; coordinator handover/takeover is Phase C.

## Verification

- AC1: with HEAD behind the gitlink and the worktree identical to the gitlink
  tree, realign moves HEAD to the descendant commit, the worktree stays
  byte-identical and no object is written.
- AC2: classification writes no objects and no worktree files; the undo record
  (`git reset --mixed -q <old>`) restores HEAD and index exactly.
- AC7: locked, staged, unmerged and missing-object repos are skipped and
  reported with a reason; the run continues.
- Real-git fixtures only, in `t.TempDir()`; the user's actual repositories are
  never touched. Gates: `make build`, targeted `go test`, `make lint`,
  `make docs`.
