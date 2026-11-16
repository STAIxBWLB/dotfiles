## dot sync shared

Manage manual shared-folder exclusions

### Synopsis

View and manage which folders dot sync skips because they are shared.

This list contains relative paths the operator added to the workspace-local
sync config. Use it for owned-but-shared-out folders that must never be
propagated through the workspace-authoritative mirror flow.

The list feeds a per-run dynamic excludes file passed to rsync. Without a
subcommand it lists, the same as 'list'.

```
dot sync shared [flags]
```

### Examples

```
  dot sync shared list
  dot sync shared add projects/shared-with-partner
```

### Options

```
  -h, --help   help for shared
```

### Options inherited from parent commands

```
      --ascii           use ASCII markers regardless of locale
      --color string    color output: auto, always or never (default "auto")
      --config string   path to custom config YAML
      --dry-run         show what would be done without executing
      --home string     override home directory (for admin setup of other users)
      --store string    sync store under <workspace>/.dotfiles/<store>/; "sync" is the cloud mirror, "peer" the other Mac (default "sync")
  -V, --verbose         show rsync progress output
      --yes             unattended mode (skip all prompts)
```

### SEE ALSO

* [dot sync](dot_sync.md)	 - Sync workspace to a local mirror or SSH remote via rsync
* [dot sync shared add](dot_sync_shared_add.md)	 - Add one or more paths to the manual shared-excludes list
* [dot sync shared clear](dot_sync_shared_clear.md)	 - Empty the manual shared-excludes list
* [dot sync shared list](dot_sync_shared_list.md)	 - Show manual shared entries
* [dot sync shared remove](dot_sync_shared_remove.md)	 - Remove paths from the shared-excludes list

