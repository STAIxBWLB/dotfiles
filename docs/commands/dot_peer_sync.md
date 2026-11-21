## dot peer sync

Exchange workspace and host paths with the peer

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

### Examples

```
  dot peer sync
  dot peer sync --pull-only
```

### Options

```
      --approve-deletes string   run one reviewed deletion set over max_delete, named by the digest a refused run prints
  -h, --help[=true|false]        help for sync
      --json[=true|false]        print the itemized plan as JSON
      --list[=true|false]        print every planned action (with --dry-run: exactly what a run would do)
      --pull-only[=true|false]   receive peer changes without pushing
      --push-only[=true|false]   send local changes without pulling first
      --skip-home[=true|false]   workspace only; skip the host-path pass
```

### Options inherited from parent commands

```
      --ascii[=true|false]     use ASCII markers regardless of locale
      --color string           color output: auto, always or never (default "auto")
      --config string          path to custom config YAML
      --dry-run[=true|false]   show what would be done without executing
      --home string            override home directory (for admin setup of other users)
      --verbose[=true|false]   show library and debug logs on stderr
      --yes[=true|false]       unattended mode (skip all prompts)
```

### SEE ALSO

* [dot peer](dot_peer.md)	 - Sync the workspace directly to another machine over SSH

