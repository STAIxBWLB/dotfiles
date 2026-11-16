## dot sync conflicts prune

Remove old conflict backups from selected local/remote trees

```
dot sync conflicts prune [flags]
```

### Options

```
      --all              prune every backup regardless of age
  -h, --help             help for prune
      --older-than int   prune backups older than this many days (default 30)
      --remote-only      prune only SSH target backups; preserve local workspace backups
```

### Options inherited from parent commands

```
      --ascii           use ASCII markers regardless of locale
      --color string    color output: auto, always or never (default "auto")
      --config string   path to custom config YAML
      --dry-run         show what would be done without executing
      --home string     override home directory (for admin setup of other users)
      --store string    sync store under <workspace>/.dotfiles/<store>/; "sync" is the cloud mirror, "peer" the other Mac (default "sync")
  -V, --verbose         Show rsync progress output
      --yes             unattended mode (skip all prompts)
```

### SEE ALSO

* [dot sync conflicts](dot_sync_conflicts.md)	 - List or prune .sync-conflicts/ backup directories

