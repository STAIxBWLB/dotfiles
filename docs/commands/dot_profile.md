## dot profile

Snapshot and restore dot profile state (config, app lists, secrets)

### Synopsis

Manage per-host profile snapshots under <backup-root>/profiles/<hostname>/<version>/.

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
      --color string    Color output: auto, always or never (default "auto")
      --config string   Path to custom config YAML
      --dry-run         Show what would be done without executing
      --home string     Override home directory (for admin setup of other users)
      --verbose         Show library and debug logs on stderr
      --yes             Unattended mode (skip all prompts)
```

### SEE ALSO

* [dot](dot.md)	 - User environment & workspace management tool
* [dot profile backup](dot_profile_backup.md)	 - Advanced: snapshot only the profile (dot backup is the front door)
* [dot profile list](dot_profile_list.md)	 - List available profile snapshots for this host
* [dot profile prune](dot_profile_prune.md)	 - Delete older profile snapshots (profiles/ only), keeping the newest N
* [dot profile restore](dot_profile_restore.md)	 - Advanced: restore only a profile snapshot (dot restore is the front door)
* [dot profile root](dot_profile_root.md)	 - Show or set the shared backup root for profiles and app-settings

