## dot ai restore

Restore only AI settings (dot restore is the front door)

```
dot ai restore [flags]
```

### Examples

```
  dot ai restore
  dot ai restore --host laptop --version latest
```

### Options

```
      --from string      backup root (overrides configured BackupRoot)
  -h, --help             help for restore
      --host string      source hostname to restore from (default: this host)
      --include-auth     restore auth/local-secret files from the snapshot
      --reapply-agents   after restore, reapply the agents SSOT to tool targets
      --version string   specific version to restore, or "latest" (default: latest)
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

