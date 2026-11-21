## dot sync inbox

Inspect and manage the mirror intake staging area

### Synopsis

View what's staged + tracked under .dotfiles/sync/, force a
re-intake of one path, or clear the imports + tombstones manifests
entirely.

Without a subcommand it lists, the same as 'list'.

```
dot sync inbox [flags]
```

### Examples

```
  dot sync inbox list
  dot sync inbox forget notes/todo.md
```

### Options

```
  -h, --help[=true|false]   help for inbox
```

### Options inherited from parent commands

```
      --ascii[=true|false]     use ASCII markers regardless of locale
      --color string           color output: auto, always or never (default "auto")
      --config string          path to custom config YAML
      --dry-run[=true|false]   show what would be done without executing
      --home string            override home directory (for admin setup of other users)
      --store string           sync store under <workspace>/.dotfiles/<store>/; "sync" is the cloud mirror, "peer" the other Mac (default "sync")
  -V, --verbose[=true|false]   show rsync progress output
      --yes[=true|false]       unattended mode (skip all prompts)
```

### SEE ALSO

* [dot sync](dot_sync.md)	 - Sync workspace to a local mirror or SSH remote via rsync
* [dot sync inbox clear](dot_sync_inbox_clear.md)	 - Empty imports.manifest and tombstones.log
* [dot sync inbox forget](dot_sync_inbox_forget.md)	 - Drop a path so the next intake re-stages it
* [dot sync inbox list](dot_sync_inbox_list.md)	 - Show staged run-dirs, imports and tombstones

