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
  -h, --help              help for backup
      --include-age-key   copy ~/.ssh/age_key* into the snapshot
      --tag string        human-friendly label stored in meta.yaml
      --to string         backup root (overrides configured BackupRoot)
```

### Options inherited from parent commands

```
      --ascii           use ASCII markers regardless of locale
      --color string    color output: auto, always or never (default "auto")
      --config string   path to custom config YAML
      --dry-run         show what would be done without executing
      --home string     override home directory (for admin setup of other users)
      --verbose         show library and debug logs on stderr
      --yes             unattended mode (skip all prompts)
```

### SEE ALSO

* [dot profile](dot_profile.md)	 - Snapshot and restore dot profile state

