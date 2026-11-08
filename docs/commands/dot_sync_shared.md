## dot sync shared

Manage manual shared-folder exclusions

### Synopsis

View and manage which folders dot sync skips because they are shared.

This list contains relative paths the operator added to the workspace-local
sync config. Use it for owned-but-shared-out folders that must never be
propagated through the workspace-authoritative mirror flow.

The list feeds a per-run dynamic excludes file passed to rsync.

  dot sync shared             # alias for list
  dot sync shared list
  dot sync shared add <path>...
  dot sync shared remove <path>...
  dot sync shared clear

```
dot sync shared [flags]
```

### Options

```
  -h, --help   help for shared
```

### Options inherited from parent commands

```
      --config string   Path to custom config YAML
      --dry-run         Show what would be done without executing
      --home string     Override home directory (for admin setup of other users)
      --store string    sync store under <workspace>/.dotfiles/<store>/; "sync" is the cloud mirror, "peer" the other Mac (default "sync")
  -V, --verbose         Show rsync progress output
      --yes             Unattended mode (skip all prompts)
```

### SEE ALSO

* [dot sync](dot_sync.md)	 - Sync workspace to a local mirror or SSH remote via rsync
* [dot sync shared add](dot_sync_shared_add.md)	 - Add one or more paths to the manual shared-excludes list
* [dot sync shared clear](dot_sync_shared_clear.md)	 - Empty the manual shared-excludes list
* [dot sync shared list](dot_sync_shared_list.md)	 - Show manual shared entries
* [dot sync shared remove](dot_sync_shared_remove.md)	 - Remove one or more paths from the manual shared-excludes list

