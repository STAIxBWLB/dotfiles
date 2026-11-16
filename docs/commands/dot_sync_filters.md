## dot sync filters

Show or reset the effective filter layers

```
dot sync filters [flags]
```

### Examples

```
  dot sync filters
  dot sync filters show
```

### Options

```
      --filter-mode string   override config filter mode for this run: include or exclude
  -h, --help                 help for filters
```

### Options inherited from parent commands

```
      --ascii           use ASCII markers regardless of locale
      --color string    color output: auto, always or never (default "auto")
      --config string   path to custom config YAML
      --dry-run         show what would be done without executing
      --home string     override home directory (for admin setup of other users)
      --store string    sync store under <workspace>/.dotfiles/<store>/; "sync" is the cloud mirror, "peer" the other Mac (default "sync")
  -V, --verbose         show rsync progress output
      --yes             unattended mode (skip all prompts)
```

### SEE ALSO

* [dot sync](dot_sync.md)	 - Sync workspace to a local mirror or SSH remote via rsync
* [dot sync filters get](dot_sync_filters_get.md)	 - Read one editable filter file
* [dot sync filters reset](dot_sync_filters_reset.md)	 - Regenerate exclude.txt and include.txt from templates
* [dot sync filters set](dot_sync_filters_set.md)	 - Replace one editable filter file from stdin
* [dot sync filters show](dot_sync_filters_show.md)	 - Print the effective ordered filter rule chain

