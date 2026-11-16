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
      --ascii           Use ASCII markers regardless of locale
      --color string    Color output: auto, always or never (default "auto")
      --config string   Path to custom config YAML
      --dry-run         Show what would be done without executing
      --home string     Override home directory (for admin setup of other users)
      --verbose         Show library and debug logs on stderr
      --yes             Unattended mode (skip all prompts)
```

### SEE ALSO

* [dot ai](dot_ai.md)	 - AI CLI/config helpers and settings backup/restore
* [dot ai backup status](dot_ai_backup_status.md)	 - Show AI settings live/backup status

