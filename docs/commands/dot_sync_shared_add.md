## dot sync shared add

Add one or more paths to the manual shared-excludes list

```
dot sync shared add <path>... [flags]
```

### Examples

```
  dot sync shared add projects/shared-with-partner
```

### Options

```
  -h, --help[=true|false]   help for add
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

* [dot sync shared](dot_sync_shared.md)	 - Manage manual shared-folder exclusions

