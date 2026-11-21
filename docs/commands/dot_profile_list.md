## dot profile list

List available profile snapshots for this host

```
dot profile list [flags]
```

### Examples

```
  dot profile list
```

### Options

```
      --from string         backup root (overrides configured BackupRoot)
  -h, --help[=true|false]   help for list
      --host string         hostname to list (default: this host)
```

### Options inherited from parent commands

```
      --ascii[=true|false]     use ASCII markers regardless of locale
      --color string           color output: auto, always or never (default "auto")
      --config string          path to custom config YAML
      --dry-run[=true|false]   show what would be done without executing
      --home string            override home directory (for admin setup of other users)
      --verbose[=true|false]   show library and debug logs on stderr
      --yes[=true|false]       unattended mode (skip all prompts)
```

### SEE ALSO

* [dot profile](dot_profile.md)	 - Snapshot and restore dot profile state

