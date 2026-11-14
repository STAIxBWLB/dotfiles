## dot sync intake

Stage new mirror-origin files for manual routing

### Synopsis

Compares the mirror against baseline.manifest and imports.manifest to
find new mirror-origin files. New candidates are copied into a timestamped
subdirectory of <local>/inbox/gdrive/<intake-ts>/ for the operator to review
and route.

Changed baseline-tracked files are skipped and left for `dot sync pull`.
Mirror-side deletions against baseline are detected by pull, not intake.

  --strict   Use sha256 fingerprints (catches content changes that
             preserve mtime). Default is fast size+mtime mode.

```
dot sync intake [flags]
```

### Options

```
      --filter-mode string   override config filter mode for this run: include or exclude
  -h, --help                 help for intake
      --strict               use sha256 fingerprints instead of size+mtime
```

### Options inherited from parent commands

```
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

