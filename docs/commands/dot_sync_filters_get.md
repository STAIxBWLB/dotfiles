## dot sync filters get

Read one editable filter file

```
dot sync filters get <include|exclude|ignore|allow> [flags]
```

### Examples

```
  dot sync filters get exclude
  dot sync filters get allow --json
```

### Options

```
  -h, --help   help for get
      --json   print a stable machine-readable document
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

