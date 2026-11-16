## dot sync

Sync workspace to a local mirror or SSH remote via rsync

### Synopsis

Unified workspace sync: rsync the workspace to a target — either a
local cloud-client folder (e.g. ~/Dropbox/work) or an SSH remote (host:path).

The sync set is a Git-aware union: every file tracked by the workspace's
root Git repo, plus untracked files matching the binary-extension allowlist.
Git submodules are never synced here — they sync through Git itself.
Secrets (.env, .secrets, ...) are excluded by default and only sync when
explicitly allowed in allow.txt.

Workspace is authoritative. Push sends local creates and updates to the
target; pull restores baseline-tracked payloads from it. New target-origin
files still stage into inbox/gdrive for manual routing.

Getting started:
  dot sync setup       Check rsync and manage the opt-in schedulers
  dot sync target      Show or set the sync target
  dot sync resume      Clear the paused gate
  dot sync push        Push workspace → target (use --mode for clean/force)
  dot sync pull        Restore baseline-tracked payloads from target

Maintenance:
  dot sync status      Show filters, last sync times, conflicts, paused state
                       and scheduler
  dot sync filters     Show effective filter layers or reset them
  dot sync conflicts   List or prune timestamped backup directories
  dot sync names       Plan or apply staged NFD filename normalization
  dot sync pause       Stop managed schedulers and set the paused gate
  dot sync resume      Clear the paused gate and re-arm installed schedulers

Run without a subcommand to print this help.

```
dot sync [flags]
```

### Examples

```
  dot sync status
  dot sync push
```

### Options

```
  -h, --help           help for sync
      --store string   sync store under <workspace>/.dotfiles/<store>/; "sync" is the cloud mirror, "peer" the other Mac (default "sync")
  -V, --verbose        show rsync progress output
```

### Options inherited from parent commands

```
      --ascii           use ASCII markers regardless of locale
      --color string    color output: auto, always or never (default "auto")
      --config string   path to custom config YAML
      --dry-run         show what would be done without executing
      --home string     override home directory (for admin setup of other users)
      --yes             unattended mode (skip all prompts)
```

### SEE ALSO

* [dot](dot.md)	 - User environment & workspace management tool
* [dot sync configure](dot_sync_configure.md)	 - Set target, owner and scheduler settings in one call
* [dot sync conflicts](dot_sync_conflicts.md)	 - List or prune .sync-conflicts/ backup directories
* [dot sync fetch](dot_sync_fetch.md)	 - Restore specific files or folders from the target on demand
* [dot sync filters](dot_sync_filters.md)	 - Show or reset the effective filter layers
* [dot sync inbox](dot_sync_inbox.md)	 - Inspect and manage the mirror intake staging area
* [dot sync init](dot_sync_init.md)	 - Initialize <workspace>/.dotfiles/sync/ from current state
* [dot sync intake](dot_sync_intake.md)	 - Stage new mirror-origin files for manual routing
* [dot sync log](dot_sync_log.md)	 - Show the tail of the profile sync log
* [dot sync names](dot_sync_names.md)	 - Maintain workspace filenames
* [dot sync owner](dot_sync_owner.md)	 - Show or set which machine may push this profile
* [dot sync pause](dot_sync_pause.md)	 - Set the Paused gate so pull/push/sync refuse to run
* [dot sync pull](dot_sync_pull.md)	 - Restore baseline-tracked mirror payloads
* [dot sync push](dot_sync_push.md)	 - Preview and send workspace changes to the mirror
* [dot sync resume](dot_sync_resume.md)	 - Clear the Paused gate so pull/push/sync can run
* [dot sync setup](dot_sync_setup.md)	 - Install rsync and manage the opt-in sync schedulers
* [dot sync shared](dot_sync_shared.md)	 - Manage manual shared-folder exclusions
* [dot sync status](dot_sync_status.md)	 - Show local↔mirror sync status
* [dot sync target](dot_sync_target.md)	 - Show or set the sync target (local mirror dir or SSH remote)

