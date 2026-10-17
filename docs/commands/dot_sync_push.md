## dot sync push

Preview and send workspace changes to mirror under a propagation policy

### Synopsis

Push the workspace tree to the gdrive mirror under a propagation
policy. The default policy '{create:true, update:true, delete:false}'
copies new and modified files but never deletes mirror-side content. By default
push prints the upload plan and asks before applying.

Flag --propagate= takes a comma-separated allowlist; absent items are
disabled. Examples:

  dot sync push                              # preview, then confirm
  dot sync push --mode=clean                 # apply only if no conflicts
  dot sync push --mode=force                 # overwrite with backups
  dot sync push --propagate=create,update,delete   # full sync
  dot sync push --propagate=create           # additive only
  dot sync push --propagate=update           # in-place updates only

The per-workspace store (.dotfiles/) and intake staging area
(inbox/gdrive/) are always excluded so they never round-trip to mirror.

Backups under the mirror's .sync-conflicts/ keep only what can be lost:
propagated deletions and overwrites of a copy changed in the mirror. A routine
update leaves none. A mirror copy with the same bytes and only a new mtime is
not a conflict.

Previews, including --dry-run, display operator-approved sensitive overrides.
Dry-run does not transfer files.

```
dot sync push [flags]
```

### Options

```
  -h, --help               help for push
      --propagate string   comma-separated allowlist of propagation kinds (create,update,delete)
```

### Options inherited from parent commands

```
      --config string        Path to custom config YAML
      --dry-run              Show what would be done without executing
      --filter-mode string   override config filter mode for this run: include or exclude
      --home string          Override home directory (for admin setup of other users)
      --mode string          execution mode for push/pull: manual, clean, or force (default "manual")
      --module strings       Run specific modules only
      --profile string       sync profile (store under <workspace>/.dotfiles/<profile>/); "sync" is the cloud mirror (default "sync")
  -V, --verbose              Show rsync progress output
      --yes                  Unattended mode (skip all prompts)
```

### SEE ALSO

* [dot sync](dot_sync.md)	 - Sync workspace to a local mirror or SSH remote via rsync

