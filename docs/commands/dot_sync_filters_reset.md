## dot sync filters reset

Regenerate exclude.txt and include.txt from templates

### Synopsis

Backs up the current exclude.txt and include.txt to *.bak-<timestamp>
and rewrites them from the embedded templates. Use after upgrading dot to
pick up refreshed junk rules.

ignore.txt and allow.txt are operator-owned and are never touched. Re-add
any workspace-specific patterns from the backups to ignore.txt afterwards.

```
dot sync filters reset [flags]
```

### Examples

```
  dot sync filters reset
```

### Options

```
  -h, --help   help for reset
```

### Options inherited from parent commands

```
      --ascii                use ASCII markers regardless of locale
      --color string         color output: auto, always or never (default "auto")
      --config string        path to custom config YAML
      --dry-run              show what would be done without executing
      --filter-mode string   override config filter mode for this run: include or exclude
      --home string          override home directory (for admin setup of other users)
      --store string         sync store under <workspace>/.dotfiles/<store>/; "sync" is the cloud mirror, "peer" the other Mac (default "sync")
  -V, --verbose              show rsync progress output
      --yes                  unattended mode (skip all prompts)
```

### SEE ALSO

* [dot sync filters](dot_sync_filters.md)	 - Show or reset the effective filter layers

