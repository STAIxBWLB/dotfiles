## dot sync conflicts

List or prune .sync-conflicts/ backup directories

### Synopsis

Conflict backups accumulate in both trees: pull backups under the
workspace, push backups under the mirror. For SSH targets, push backups are
listed and pruned on the remote target under <target>/.sync-conflicts; the
peer profile also includes ~/.dot-peer-conflicts from its host-path pass.

Without a subcommand it lists, the same as 'list'.

```
dot sync conflicts [flags]
```

### Examples

```
  dot sync conflicts list
  dot sync conflicts prune --older-than 7
```

### Options

```
  -h, --help   help for conflicts
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
* [dot sync conflicts list](dot_sync_conflicts_list.md)	 - List .sync-conflicts/ backup directories in both trees
* [dot sync conflicts prune](dot_sync_conflicts_prune.md)	 - Remove old conflict backups from selected local/remote trees

