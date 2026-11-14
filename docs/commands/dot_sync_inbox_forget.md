## dot sync inbox forget

Drop a path from imports.manifest so the next intake re-stages it

```
dot sync inbox forget <relpath> [flags]
```

### Options

```
  -h, --help   help for forget
```

### Options inherited from parent commands

```
      --ascii           Use ASCII markers regardless of locale
      --color string    Color output: auto, always or never (default "auto")
      --config string   Path to custom config YAML
      --dry-run         Show what would be done without executing
      --home string     Override home directory (for admin setup of other users)
      --store string    sync store under <workspace>/.dotfiles/<store>/; "sync" is the cloud mirror, "peer" the other Mac (default "sync")
  -V, --verbose         Show rsync progress output
      --yes             Unattended mode (skip all prompts)
```

### SEE ALSO

* [dot sync inbox](dot_sync_inbox.md)	 - Inspect and manage the mirror intake staging area

