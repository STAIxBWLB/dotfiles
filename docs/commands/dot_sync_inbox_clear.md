## dot sync inbox clear

Empty imports.manifest and tombstones.log

```
dot sync inbox clear [flags]
```

### Examples

```
  dot sync inbox clear
```

### Options

```
  -h, --help   help for clear
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

* [dot sync inbox](dot_sync_inbox.md)	 - Inspect and manage the mirror intake staging area

