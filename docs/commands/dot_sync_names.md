## dot sync names

Maintain workspace filenames

```
dot sync names [flags]
```

### Examples

```
  dot sync names
```

### Options

```
      --filter-mode string   override config filter mode for this run: include or exclude
  -h, --help[=true|false]    help for names
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
* [dot sync names normalize](dot_sync_names_normalize.md)	 - Plan or apply Unicode NFD filename normalization
* [dot sync names trim](dot_sync_names_trim.md)	 - Plan or apply trailing-whitespace filename trims

