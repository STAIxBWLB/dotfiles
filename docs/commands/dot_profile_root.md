## dot profile root

Show or set the shared backup root

### Synopsis

Display or change the backup root directory.

With no arguments, prints the current effective root (state → auto-detect →
default). With a path argument, saves it to state. Use --detect to
auto-discover a Dropbox or Google Drive secrets folder, or --reset to clear the
saved value and fall back to auto-detection.

```
dot profile root [path] [flags]
```

### Examples

```
  dot profile root
  dot profile root --detect
```

### Options

```
      --detect   auto-detect Dropbox/Google Drive secrets folder and save
  -h, --help     help for root
      --reset    clear saved root (revert to auto-detect / default)
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

