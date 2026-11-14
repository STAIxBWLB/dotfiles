## dot ai restore

Advanced: restore only AI settings (dot restore is the front door)

```
dot ai restore [flags]
```

### Options

```
      --from string      Backup root (overrides configured BackupRoot)
  -h, --help             help for restore
      --host string      Source hostname to restore from (default: this host)
      --include-auth     Restore auth/local-secret files from the snapshot
      --reapply-agents   After restore, reapply the agents SSOT to tool targets
      --version string   Specific version to restore, or "latest" (default: latest)
```

### Options inherited from parent commands

```
      --color string    Color output: auto, always or never (default "auto")
      --config string   Path to custom config YAML
      --dry-run         Show what would be done without executing
      --home string     Override home directory (for admin setup of other users)
      --verbose         Show library and debug logs on stderr
      --yes             Unattended mode (skip all prompts)
```

### SEE ALSO

* [dot ai](dot_ai.md)	 - AI CLI/config helpers and settings backup/restore

