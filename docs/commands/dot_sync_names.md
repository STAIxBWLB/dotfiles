## dot sync names

Maintain workspace filenames (Unicode NFD, trailing whitespace)

```
dot sync names [flags]
```

### Options

```
      --filter-mode string   override config filter mode for this run: include or exclude
  -h, --help                 help for names
```

### Options inherited from parent commands

```
      --config string   Path to custom config YAML
      --dry-run         Show what would be done without executing
      --home string     Override home directory (for admin setup of other users)
      --store string    sync store under <workspace>/.dotfiles/<store>/; "sync" is the cloud mirror, "peer" the other Mac (default "sync")
  -V, --verbose         Show rsync progress output
      --yes             Unattended mode (skip all prompts)
```

### SEE ALSO

* [dot sync](dot_sync.md)	 - Sync workspace to a local mirror or SSH remote via rsync
* [dot sync names normalize](dot_sync_names_normalize.md)	 - Plan or apply Unicode NFD filename normalization
* [dot sync names trim](dot_sync_names_trim.md)	 - Plan or apply trailing-whitespace filename trims

