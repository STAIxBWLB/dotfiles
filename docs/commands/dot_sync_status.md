## dot sync status

Show local↔mirror sync status

### Synopsis

Show local↔mirror sync status.

Status identifies each allow.txt rule that re-includes a built-in secret
exclusion. --json exposes the equivalent versioned structured records.

```
dot sync status [flags]
```

### Examples

```
  dot sync status
  dot sync status --json
```

### Options

```
      --filter-mode string   override config filter mode for this run: include or exclude
  -h, --help[=true|false]    help for status
      --json[=true|false]    print a stable machine-readable status document
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

