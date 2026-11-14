## dot sync fetch

Restore specific files or folders from the target on demand

### Synopsis

Fetch pulls the named files or directories (workspace-relative paths)
from the sync target into the workspace — the on-demand entry point when a
specific file, folder, program, or event needs the binaries backing a path
without running a full pull. Other tools can shell out to it:

  dot sync fetch projects/oda/koica-tiu/06-proposal
  dot sync fetch admin/scan.pdf research/data --dry-run

Safety: newer local files are never overwritten (--update), overwrites are
backed up under .sync-conflicts/, nothing is deleted, and the exclude layers
still apply — .git and non-allowed secrets can never be imported. Paths
missing on the target, and paths a sync filter keeps out (submodules, .git,
secrets, excludes, shared folders), are reported and skipped. An absolute path is accepted
when it lies under the workspace.

```
dot sync fetch <path>... [flags]
```

### Options

```
      --filter-mode string   override config filter mode for this run: include or exclude
  -h, --help                 help for fetch
```

### Options inherited from parent commands

```
      --ascii           Use ASCII markers regardless of locale
      --color string    Color output: auto, always or never (default "auto")
      --config string   Path to custom config YAML
      --dry-run         Show what would be done without executing
      --home string     Override home directory (for admin setup of other users)
      --store string    sync store under <workspace>/.dotfiles/<store>/; "sync" is the cloud mirror, "peer" the other Mac (default "sync")
  -V, --verbose         Show rsync progress output
      --yes             Unattended mode (skip all prompts)
```

### SEE ALSO

* [dot sync](dot_sync.md)	 - Sync workspace to a local mirror or SSH remote via rsync

