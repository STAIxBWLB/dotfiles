## dot apps backup

Snapshot only macOS app settings

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
  - no args + --yes       : use saved state (falls back to manifest ∩
                            installed).

```
dot apps backup [token...] [flags]
```

### Examples

```
  dot apps backup --all
  dot apps backup raycast
```

### Options

```
      --all[=true|false]       back up every manifest entry (default: manifest ∩ installed casks)
  -h, --help[=true|false]      help for backup
      --no-save[=true|false]   do not persist the interactive selection back to state
      --select[=true|false]    force the interactive picker even when state has a list
      --to string              backup root (overrides configured BackupDir)
```

### Options inherited from parent commands

```
      --ascii[=true|false]     use ASCII markers regardless of locale
      --color string           color output: auto, always or never (default "auto")
      --config string          path to custom config YAML
      --dry-run[=true|false]   show what would be done without executing
      --home string            override home directory (for admin setup of other users)
      --verbose[=true|false]   show library and debug logs on stderr
      --yes[=true|false]       unattended mode (skip all prompts)
```

### SEE ALSO

* [dot apps](dot_apps.md)	 - Install macOS apps and back up their settings

