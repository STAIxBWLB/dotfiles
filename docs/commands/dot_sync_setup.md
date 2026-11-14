## dot sync setup

Install rsync (if missing) and manage the opt-in sync schedulers

### Synopsis

One-time setup. Verifies rsync is available (offers to install via brew/apt
if not), then configures the platform's user-scheduler (launchd LaunchAgent on
macOS, systemd user-timer on Linux). Automatic sync is off by default; pass an
interval flag to opt in.

  --push-interval=DUR    Deploy automatic `dot sync push --mode=MODE`.
  --pull-interval=DUR    Deploy automatic `dot sync pull --mode=MODE`.
  --push-mode=MODE       Automatic push mode: clean or force (default clean).
  --pull-mode=MODE       Automatic intake mode: clean or force (default clean).
  --owner=NAME           Record the machine allowed to run this scheduler; use self for this machine.

Idempotent — re-run safely after an interval change to reload the unit.

```
dot sync setup [flags]
```

### Options

```
  -h, --help                   help for setup
      --owner string           machine allowed to run this scheduler (self or a machine name)
      --pull-interval string   deploy pull scheduler at this cadence (e.g. 15m, 1h, 0 to remove)
      --pull-mode string       automatic intake mode: clean or force (default "clean")
      --push-interval string   deploy push scheduler at this cadence (e.g. 15m, 1h, 0 to remove)
      --push-mode string       automatic push mode: clean or force (default "clean")
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

