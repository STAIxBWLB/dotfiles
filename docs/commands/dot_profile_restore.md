## dot profile restore

Restore only a profile snapshot

```
dot profile restore [flags]
```

### Examples

```
  dot profile restore
  dot profile restore --version 2.83.0
```

### Options

```
      --from string       backup root (overrides configured BackupRoot)
  -h, --help              help for restore
      --host string       source hostname to restore from (default: this host)
      --include-age-key   restore ~/.ssh/age_key* from the snapshot if present
      --no-state          skip copying config.yaml back to ~/.config/dotfiles/
      --version string    specific version to restore (default: latest)
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

* [dot profile](dot_profile.md)	 - Snapshot and restore dot profile state

