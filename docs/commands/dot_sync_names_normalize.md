## dot sync names normalize

Plan or apply Unicode NFD filename normalization

```
dot sync names normalize [flags]
```

### Examples

```
  dot sync names normalize
```

### Options

```
  -h, --help[=true|false]   help for normalize
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

* [dot sync names](dot_sync_names.md)	 - Maintain workspace filenames

