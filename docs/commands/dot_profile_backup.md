## dot profile backup

Advanced: snapshot only the profile (dot backup is the front door)

```
dot profile backup [flags]
```

### Options

```
  -h, --help              help for backup
      --include-age-key   Copy ~/.ssh/age_key* into the snapshot
      --tag string        Human-friendly label stored in meta.yaml
      --to string         Backup root (overrides configured BackupRoot)
```

### Options inherited from parent commands

```
      --config string   Path to custom config YAML
      --dry-run         Show what would be done without executing
      --home string     Override home directory (for admin setup of other users)
      --yes             Unattended mode (skip all prompts)
```

### SEE ALSO

* [dot profile](dot_profile.md)	 - Snapshot and restore dot profile state (config, app lists, secrets)

