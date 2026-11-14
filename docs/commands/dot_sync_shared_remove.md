## dot sync shared remove

Remove one or more paths from the manual shared-excludes list

```
dot sync shared remove <path>... [flags]
```

### Options

```
  -h, --help   help for remove
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

* [dot sync shared](dot_sync_shared.md)	 - Manage manual shared-folder exclusions

