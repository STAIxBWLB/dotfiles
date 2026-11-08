## dot ai backup

Advanced: snapshot only AI settings (dot backup is the front door)

```
dot ai backup [flags]
```

### Options

```
  -h, --help           help for backup
      --include-auth   Include auth/local-secret files
      --tag string     Human-friendly label stored in meta.yaml
      --to string      Backup root (overrides configured BackupRoot)
```

### Options inherited from parent commands

```
      --config string   Path to custom config YAML
      --dry-run         Show what would be done without executing
      --home string     Override home directory (for admin setup of other users)
      --yes             Unattended mode (skip all prompts)
```

### SEE ALSO

* [dot ai](dot_ai.md)	 - AI CLI/config helpers and settings backup/restore
* [dot ai backup status](dot_ai_backup_status.md)	 - Show AI settings live/backup status

