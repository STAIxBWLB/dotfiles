## dot profile

Snapshot and restore dot profile state

### Synopsis

Manage per-host profile snapshots under
<backup-root>/profiles/<hostname>/<version>/.

Each snapshot captures:
  - config.yaml          → ~/.config/dotfiles/config.yaml
  - apps/install.yaml    → install list (casks + casks_extra)
  - apps/backup.yaml     → backup list + backup root
  - meta.yaml            → timestamp, tag, hostname, user
  - secrets/             → optional copy of ~/.ssh/age_key* (--include-age-key)

The shared backup root is resolved via --to/--from, the user state
(BackupRoot), an auto-detected Drive "secrets" folder, or a local default.

### Options

```
  -h, --help   help for profile
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

* [dot](dot.md)	 - User environment & workspace management tool
* [dot profile backup](dot_profile_backup.md)	 - Snapshot only the profile
* [dot profile list](dot_profile_list.md)	 - List available profile snapshots for this host
* [dot profile prune](dot_profile_prune.md)	 - Delete older profile snapshots, keeping the newest N
* [dot profile restore](dot_profile_restore.md)	 - Restore only a profile snapshot
* [dot profile root](dot_profile_root.md)	 - Show or set the shared backup root

