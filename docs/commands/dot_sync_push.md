## dot sync push

Preview and send workspace changes to the mirror

### Synopsis

Push the workspace tree to the sync target (local: or ssh:) under a propagation
policy. The default policy '{create:true, update:true, delete:false}'
copies new and modified files but never deletes mirror-side content. By default
push prints the upload plan and asks before applying.

Flag --propagate= takes a comma-separated allowlist; absent items are
disabled.

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

### Examples

```
  dot sync push
  dot sync push --propagate=create,update,delete
```

### Options

```
      --filter-mode string   override config filter mode for this run: include or exclude
  -h, --help[=true|false]    help for push
      --mode string          execution mode for push/pull: manual, clean, or force (default "manual")
      --propagate string     comma-separated allowlist of propagation kinds (create,update,delete)
```

### Options inherited from parent commands

```
      --ascii[=true|false]     use ASCII markers regardless of locale
      --color string           color output: auto, always or never (default "auto")
      --config string          path to custom config YAML
      --dry-run[=true|false]   show what would be done without executing
      --home string            override home directory (for admin setup of other users)
      --store string           sync store under <workspace>/.dotfiles/<store>/; "sync" is the cloud mirror, "peer" the other Mac (default "sync")
  -V, --verbose[=true|false]   show rsync progress output
      --yes[=true|false]       unattended mode (skip all prompts)
```

### SEE ALSO

* [dot sync](dot_sync.md)	 - Sync workspace to a local mirror or SSH remote via rsync

