## dot ai backup

Snapshot only AI settings (dot backup is the front door)

```
dot ai backup [flags]
```

### Examples

```
  dot ai backup
  dot ai backup --tag before-upgrade
```

### Options

```
  -h, --help           help for backup
      --include-auth   include auth/local-secret files
      --tag string     human-friendly label stored in meta.yaml
      --to string      backup root (overrides configured BackupRoot)
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

* [dot ai](dot_ai.md)	 - AI CLI/config helpers and settings backup/restore
* [dot ai backup status](dot_ai_backup_status.md)	 - Show AI settings live/backup status

