## dot peer sync

Exchange workspace and host paths with the peer (both directions)

### Synopsis

Build one baseline-aware plan, accept one-sided peer changes, then
publish coordinator changes. Routine updates are backup-free; only simultaneous
conflicts and propagated deletes create quarantine copies.

When propagation.delete is on, baseline-proven deletes can flow in either
direction. Unknown peer-created paths are pulled and never classified as local
deletions. The common baseline advances only after the complete workspace and
host-path transaction succeeds.

Tracked host paths (home-paths-tracked.txt) run their own baseline-aware
transaction before the additive host-path pass: deletes propagate into the
receiving side's ~/.dot-peer-conflicts quarantine and dual edits keep both
versions. Untracked host paths keep additive newest-mtime-wins behavior.

A plan with more deletions than max_delete is refused and names a digest of
its exact deletion set. After reviewing it (dot peer diff --list), approve that
set for one run with --approve-deletes <digest>; if the set changed since,
the run refuses again with the new digest. The config is never edited.

Exits 0 when the peer is unreachable. That is what makes this safe to schedule
on a laptop.

```
dot peer sync [flags]
```

### Options

```
      --approve-deletes string   run one reviewed deletion set over max_delete, named by the digest a refused run prints
  -h, --help                     help for sync
      --json                     print the itemized plan as JSON
      --list                     print every planned action (with --dry-run: exactly what a run would do)
      --pull-only                receive peer changes without pushing
      --push-only                send local changes without pulling first
      --skip-home                workspace only; skip the host-path pass
```

### Options inherited from parent commands

```
      --config string    Path to custom config YAML
      --dry-run          Show what would be done without executing
      --home string      Override home directory (for admin setup of other users)
      --module strings   Run specific modules only
      --profile string   Profile name (minimal, full, server)
      --yes              Unattended mode (skip all prompts)
```

### SEE ALSO

* [dot peer](dot_peer.md)	 - Sync the workspace directly to another machine over SSH

