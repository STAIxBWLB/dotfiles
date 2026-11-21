## dot sync log

Show the tail of the profile sync log

```
dot sync log [flags]
```

### Examples

```
  dot sync log
  dot sync log --tail 50
```

### Options

```
  -h, --help[=true|false]   help for log
      --json[=true|false]   print a stable machine-readable document
      --tail int            maximum number of newest lines (default 200)
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

