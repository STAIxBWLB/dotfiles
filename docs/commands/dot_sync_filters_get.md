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
  -h, --help[=true|false]   help for get
      --json[=true|false]   print a stable machine-readable document
```

### Options inherited from parent commands

```
      --ascii[=true|false]     use ASCII markers regardless of locale
      --color string           color output: auto, always or never (default "auto")
      --config string          path to custom config YAML
      --dry-run[=true|false]   show what would be done without executing
      --filter-mode string     override config filter mode for this run: include or exclude
      --home string            override home directory (for admin setup of other users)
      --store string           sync store under <workspace>/.dotfiles/<store>/; "sync" is the cloud mirror, "peer" the other Mac (default "sync")
  -V, --verbose[=true|false]   show rsync progress output
      --yes[=true|false]       unattended mode (skip all prompts)
```

### SEE ALSO

* [dot sync filters](dot_sync_filters.md)	 - Show or reset the effective filter layers

