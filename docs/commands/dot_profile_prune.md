## dot profile prune

Delete older profile snapshots (profiles/ only), keeping the newest N

```
dot profile prune [flags]
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

* [dot profile](dot_profile.md)	 - Snapshot and restore dot profile state (config, app lists, secrets)

