## dot sync pull

Restore/update baseline-tracked mirror payloads into the workspace

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

### Options

```
      --filter-mode string   override config filter mode for this run: include or exclude
  -h, --help                 help for pull
      --mode string          execution mode for push/pull: manual, clean, or force (default "manual")
      --strict               force sha256 fingerprints for every baseline entry (slower; catches content changes that preserve size+mtime)
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

* [dot sync](dot_sync.md)	 - Sync workspace to a local mirror or SSH remote via rsync

