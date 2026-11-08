## dot apps backup

Advanced: snapshot only macOS app settings (dot backup is the front door)

### Synopsis

Back up macOS application settings listed in the embedded manifest.

Modes:
  - positional args       : back up exactly those tokens.
  - --all                 : back up every manifest entry.
  - --select              : open the checkbox picker even when state has a list.
  - no args + interactive : open the checkbox picker. The list shows the
                            installed casks that also have a manifest entry,
                            plus any custom tokens you added previously.
                            Apps with an existing backup snapshot (or in your
                            saved selection) come pre-ticked. You can also
                            type extra tokens; each is validated against the
                            manifest before being accepted.
  - no args + --yes       : use saved state (falls back to manifest ∩ installed).

```
dot apps backup [token...] [flags]
```

### Options

```
      --all         Back up every manifest entry (default: manifest ∩ installed casks)
  -h, --help        help for backup
      --no-save     Do not persist the interactive selection back to state
      --select      Force the interactive picker even when state has a list
      --to string   Backup root (overrides configured BackupDir)
```

### Options inherited from parent commands

```
      --config string   Path to custom config YAML
      --dry-run         Show what would be done without executing
      --home string     Override home directory (for admin setup of other users)
      --yes             Unattended mode (skip all prompts)
```

### SEE ALSO

* [dot apps](dot_apps.md)	 - macOS app install and settings backup/restore

