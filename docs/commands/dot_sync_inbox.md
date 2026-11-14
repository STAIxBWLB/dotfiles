## dot sync inbox

Inspect and manage the mirror intake staging area

### Synopsis

View what's staged + tracked under .dotfiles/sync/, force a
re-intake of one path, or clear the imports + tombstones manifests
entirely.

  dot sync inbox                  # alias for list
  dot sync inbox list
  dot sync inbox forget <relpath> # next intake re-stages this path
  dot sync inbox clear            # empty imports + tombstones

```
dot sync inbox [flags]
```

### Options

```
  -h, --help   help for inbox
```

### Options inherited from parent commands

```
      --color string    Color output: auto, always or never (default "auto")
      --config string   Path to custom config YAML
      --dry-run         Show what would be done without executing
      --home string     Override home directory (for admin setup of other users)
      --store string    sync store under <workspace>/.dotfiles/<store>/; "sync" is the cloud mirror, "peer" the other Mac (default "sync")
  -V, --verbose         Show rsync progress output
      --yes             Unattended mode (skip all prompts)
```

### SEE ALSO

* [dot sync](dot_sync.md)	 - Sync workspace to a local mirror or SSH remote via rsync
* [dot sync inbox clear](dot_sync_inbox_clear.md)	 - Empty imports.manifest and tombstones.log
* [dot sync inbox forget](dot_sync_inbox_forget.md)	 - Drop a path from imports.manifest so the next intake re-stages it
* [dot sync inbox list](dot_sync_inbox_list.md)	 - Show staged run-dirs, imports manifest entries, and tombstones

