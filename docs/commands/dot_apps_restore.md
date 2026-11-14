## dot apps restore

Advanced: restore only macOS app settings (dot restore is the front door)

```
dot apps restore [token...] [flags]
```

### Options

```
      --all           Restore every manifest entry
      --from string   Backup root (overrides configured BackupDir)
  -h, --help          help for restore
      --host string   Source hostname to restore from (default: this host)
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

* [dot apps](dot_apps.md)	 - macOS app install and settings backup/restore

