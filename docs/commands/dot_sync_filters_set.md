## dot sync filters set

Replace one editable filter file from stdin

```
dot sync filters set <include|exclude|ignore|allow> [flags]
```

### Options

```
      --ack-secret-exposure   acknowledge that added allow patterns send matching secrets
  -h, --help                  help for set
      --json                  print the updated file as JSON
```

### Options inherited from parent commands

```
      --config string        Path to custom config YAML
      --dry-run              Show what would be done without executing
      --filter-mode string   override config filter mode for this run: include or exclude
      --home string          Override home directory (for admin setup of other users)
      --store string         sync store under <workspace>/.dotfiles/<store>/; "sync" is the cloud mirror, "peer" the other Mac (default "sync")
  -V, --verbose              Show rsync progress output
      --yes                  Unattended mode (skip all prompts)
```

### SEE ALSO

* [dot sync filters](dot_sync_filters.md)	 - Show the effective filter layers or reset them from templates

