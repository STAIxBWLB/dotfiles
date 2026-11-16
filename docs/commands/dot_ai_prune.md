## dot ai prune

Delete older AI snapshots, keeping the newest N

```
dot ai prune [flags]
```

### Examples

```
  dot ai prune
  dot ai prune --keep 3
```

### Options

```
      --from string   backup root (overrides configured BackupRoot)
  -h, --help          help for prune
      --host string   hostname to prune (default: this host)
      --keep int      number of most recent snapshots to keep (default 5)
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

