## dot sync init

Initialize <workspace>/.dotfiles/sync/ from current state

### Synopsis

One-time onboarding for the per-workspace store. Creates
<workspace>/.dotfiles/sync/ with config.yaml, include.txt, exclude.txt,
ignore.txt, manifests, log dir; appends '/.dotfiles/' to <workspace>/.gitignore
so the store is never committed; and creates <workspace>/inbox/gdrive/ if
missing.

Idempotent — re-running on a populated store leaves operator edits intact and
just heals any missing pieces.

```
dot sync init [flags]
```

### Examples

```
  dot sync init
```

### Options

```
  -h, --help   help for init
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

