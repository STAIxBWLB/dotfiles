## dot sync filters

Show the effective filter layers or reset them from templates

```
dot sync filters [flags]
```

### Options

```
      --filter-mode string   override config filter mode for this run: include or exclude
  -h, --help                 help for filters
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
* [dot sync filters get](dot_sync_filters_get.md)	 - Read one editable filter file
* [dot sync filters reset](dot_sync_filters_reset.md)	 - Regenerate exclude.txt/include.txt from embedded templates (with backups)
* [dot sync filters set](dot_sync_filters_set.md)	 - Replace one editable filter file from stdin
* [dot sync filters show](dot_sync_filters_show.md)	 - Print the effective ordered filter rule chain

