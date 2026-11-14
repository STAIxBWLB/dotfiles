## dot profile root

Show or set the shared backup root for profiles and app-settings

### Synopsis

Display or change the backup root directory.

With no arguments, prints the current effective root (state → auto-detect → default).
With a path argument, saves it to state. Use --detect to auto-discover a Dropbox
or Google Drive secrets folder, or --reset to clear the saved value and fall back to auto-detection.

```
dot profile root [path] [flags]
```

### Options

```
      --detect   Auto-detect Dropbox/Google Drive secrets folder and save
  -h, --help     help for root
      --reset    Clear saved root (revert to auto-detect / default)
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

* [dot profile](dot_profile.md)	 - Snapshot and restore dot profile state (config, app lists, secrets)

