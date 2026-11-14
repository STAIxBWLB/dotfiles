## dot profile restore

Advanced: restore only a profile snapshot (dot restore is the front door)

```
dot profile restore [flags]
```

### Options

```
      --from string       Backup root (overrides configured BackupRoot)
  -h, --help              help for restore
      --host string       Source hostname to restore from (default: this host)
      --include-age-key   Restore ~/.ssh/age_key* from the snapshot if present
      --no-state          Skip copying config.yaml back to ~/.config/dotfiles/
      --version string    Specific version to restore (default: latest)
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

* [dot profile](dot_profile.md)	 - Snapshot and restore dot profile state (config, app lists, secrets)

