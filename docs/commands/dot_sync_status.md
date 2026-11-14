## dot sync status

Show local↔mirror sync status

### Synopsis

Show local↔mirror sync status.

Status identifies each allow.txt rule that re-includes a built-in secret
exclusion. --json exposes the equivalent versioned structured records.

```
dot sync status [flags]
```

### Options

```
      --filter-mode string   override config filter mode for this run: include or exclude
  -h, --help                 help for status
      --json                 print a stable machine-readable status document
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

