## dot sync pull

Restore baseline-tracked mirror payloads

### Synopsis

Pull applies mirror-side changes only for paths listed in
.dotfiles/sync/baseline.manifest. Baseline is expected to be tracked in
Git, so a second machine can git pull the index and then restore binary
payloads from the cloud mirror.

Files absent from baseline are not copied into the workspace by pull; run
intake to stage new mirror-origin files under inbox/gdrive/<ts>/ for manual
review. If local and mirror both changed a baseline-tracked file, manual mode
asks before applying, clean mode aborts, and force mode overwrites local after
backing up the local version into .sync-conflicts/<ts>/from-workspace/.

```
dot sync pull [flags]
```

### Examples

```
  dot sync pull
  dot sync pull --mode clean
```

### Options

```
      --filter-mode string   override config filter mode for this run: include or exclude
  -h, --help                 help for pull
      --mode string          execution mode for push/pull: manual, clean, or force (default "manual")
      --strict               force sha256 fingerprints for every baseline entry (slower; catches content changes that preserve size+mtime)
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

