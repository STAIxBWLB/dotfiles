## dot peer git realign

Move HEAD and index to the descendant commit the files already match

### Synopsis

Move each repo's HEAD and index forward to the descendant commit its files
already match. The default is a preview; --apply moves.

A repo with no such descendant is reported as no-match with a class and the
next step:
  at-tip            at the upstream tip; only uncommitted changes differ
  ahead-unpushed    local-only commits the upstream lacks; push them (a
                    warning instead when HEAD's history has rewrite twins)
  diverged          local-only commits and upstream commits; the files match
                    an upstream commit
  rewritten-upstream
                    diverged, but each local-only commit has an upstream twin
                    with the same author, date and subject: the upstream was
                    rewritten, and a rescue stays local
  branch-mismatch   HEAD is on another branch, the files match the default
                    branch
A leftover REBASE_HEAD with no rebase in progress is skipped as
stale-rebase-head with the command that clears it.

--rescue also moves diverged (rewritten-upstream included) and
branch-mismatch repos: HEAD's commits are kept on rescue/<yymmdd>-<branch>,
pushed to the remote (--no-push keeps it local; a Git LFS repo, one the
check cannot read, or one whose push remote has a rescue branch is rescued
only with --no-push; a pushed name skips those the remote's tracking refs
hold), then HEAD and the index move to the matching commit, on the default
branch for a branch mismatch. No worktree file but .gitmodules is written;
every move prints its undo command. A rescue can still fail (a push), so a
parent that can stay does not record a commit past where its rescued child
may end, and follows on the next run; a parent whose own files need the move
names in its tie line the children it passes.

A rewritten upstream (a history rewrite that kept authors, dates and
subjects, as git filter-repo does; a rebase or amend counts too) leaves the
Mac that did not rewrite with the pre-rewrite commits as local-only work.
Pushing them would publish what the rewrite took out, so rewritten-upstream
repos are rescued only with --no-push, and so is any repo whose history has
a commit with such a twin in a remote branch's history (a commit made on top
of the old history, a branch cut from it, a feature branch whose upstream
was rewritten, the rewrite merged into the old history) or where that cannot
be checked; the old commits stay on the local rescue branch. Tags the
rewrite moved stay at the old commits until git fetch --tags --force. A
rewrite is seen only once fetched, so before a rescue push realign reads the
remote branches the rescue rests on with git ls-remote and stops before
writing anything (no rescue branch, HEAD not moved) when the branch it moves
to moved since the last fetch, is gone, is not tracked or cannot be read, or
when a branch mismatch's own upstream moved (one deleted after a squash
merge is fine). An ahead-unpushed repo whose history has such twins is
warned instead of told to push; a rewrite not yet fetched is not seen there,
and a cherry-pick or a dropped branch's tracking ref also makes a twin.

--candidate-refs <pattern> (repeatable) also takes the commits of refs
matching a git for-each-ref pattern as candidates, in every repo, for the
case where the Mac that stopped had commits it never pushed. Fetch its
branches into a namespace first, for example
  git -C <repo> fetch <that Mac's repo URL> \
      '+refs/heads/*:refs/peer/<mac>/heads/*'
then realign with --candidate-refs refs/peer/<mac>/. Such a commit must be a
strict descendant of HEAD like every candidate, and a target taken from one
names its ref. A pattern that matches nothing adds nothing.

Peer sync never carries .gitmodules. In a repo that is aligned, realigned or
at its upstream tip, a worktree .gitmodules that is missing, or equal to an
older committed version of the commit it sits on or moves to, is reported
(missing or stale); --apply restores it from HEAD and runs
git submodule sync for the URLs it moves, in children the run may touch,
printing an undo for each origin it rewrites. A URL counts as moved when git
resolves the two spellings (insteadOf applied) differently. A submodule
whose gitlink commit is missing is reported with the
fetch (and set-url, for a moved URL) commands; --apply --fetch runs them and
retries it. --apply --fetch first fetches every repo's upstream, as
'dot peer git fetch' does, so the commits the other Mac pushed are
candidates. With --apply --fetch a child that lacks a commit one of its
parent's tied candidates records is also fetched from its origin, once,
before those candidates are compared, so it is judged with the commit there
(a lone candidate the parent's own files require is taken without asking).

```
dot peer git realign [--apply [--fetch]] [--rescue [--no-push]] [--candidate-refs <pattern>]... [<repo>...] [flags]
```

### Options

```
      --apply                        move HEAD and index (default is a dry-run preview)
      --candidate-refs stringArray   also take commits of refs matching this for-each-ref pattern as candidates (repeatable), e.g. refs/peer/<mac>/ after fetching that Mac's branches there
      --fetch                        with --apply, fetch every repo's upstream before judging it, fetch a submodule whose gitlink commit is missing (following a moved URL) and retry it, and fetch a child before judging a candidate that records a commit it lacks
  -h, --help                         help for realign
      --no-push                      with --rescue, keep rescue branches local
      --rescue                       also move diverged and branch-mismatch repos, keeping HEAD on a pushed rescue/<date>-<branch> branch
```

### Options inherited from parent commands

```
      --ascii           Use ASCII markers regardless of locale
      --color string    Color output: auto, always or never (default "auto")
      --config string   Path to custom config YAML
      --dry-run         Show what would be done without executing
      --home string     Override home directory (for admin setup of other users)
      --verbose         Show library and debug logs on stderr
      --yes             Unattended mode (skip all prompts)
```

### SEE ALSO

* [dot peer git](dot_peer_git.md)	 - Realign HEAD and index with the files peer sync delivered

