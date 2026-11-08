## dot ai prune

Delete older AI snapshots (ai-config/ only), keeping the newest N

```
dot ai prune [flags]
```

### Options

```
      --from string   Backup root (overrides configured BackupRoot)
  -h, --help          help for prune
      --host string   Hostname to prune (default: this host)
      --keep int      Number of most recent snapshots to keep (default 5)
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

