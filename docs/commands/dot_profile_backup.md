## dot profile backup

Snapshot only the profile

```
dot profile backup [flags]
```

### Examples

```
  dot profile backup
  dot profile backup --tag before-upgrade
```

### Options

```
  -h, --help[=true|false]              help for backup
      --include-age-key[=true|false]   copy ~/.ssh/age_key* into the snapshot
      --tag string                     human-friendly label stored in meta.yaml
      --to string                      backup root (overrides configured BackupRoot)
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

