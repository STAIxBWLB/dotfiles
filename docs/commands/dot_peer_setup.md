## dot peer setup

Install or remove the periodic peer sync job

### Synopsis

Schedule dot peer sync.

An unreachable peer exits 0, so a laptop that is away simply produces quiet
no-op runs rather than failures. That is why this can be scheduled at all.

Pick an interval in minutes, not seconds: the payload is large and each run
walks the whole tree.

Role hooks: the machine without the coordinator role must run no jobs that
write the workspace. List them in .dotfiles/peer/config.yaml:

  hooks:
    on_deactivate:
      - launchd-bootout com.maru.job.*
      - app-quit Maru
    on_activate:
      - launchd-bootstrap com.maru.job.*
      - app-open Maru

on_activate runs after this command installs the scheduler (also the step a
handover runs on the new coordinator, and the one a takeover names next);
on_deactivate runs after --off, on the old coordinator in a handover, on a
machine that demotes itself at the fence, and after a peer dot sync owner
--set/--clear that takes the role from this Mac. The launchd actions act on the
jobs of ~/Library/LaunchAgents/<glob>.plist: launchd-bootout disables them,
so a reboot does not load them again, and records which ones it disabled;
launchd-bootstrap re-enables only those, so a job stopped outside dot stays
stopped. app-quit sends an Apple Event, which macOS allows per sending
program: in a scheduled demotion that is dot itself, which must be allowed
under Privacy & Security > Automation when it first asks; until then the
action fails and the app keeps running. --dry-run lists what each would do.
Each action has a one-minute limit. Results are printed and appended to the
peer log; a failed hook never stops the command or a sync. Upgrade dot on
both Macs before adding hooks: a dot without them runs none (and one from
before #196 drops the key when it saves; later ones keep top-level keys).

```
dot peer setup [flags]
```

### Options

```
  -h, --help                help for setup
      --interval duration   how often to sync with the peer (default 15m0s)
      --off                 remove the scheduled job
```

### Options inherited from parent commands

```
      --ascii           Use ASCII markers regardless of locale
      --color string    Color output: auto, always or never (default "auto")
      --config string   Path to custom config YAML
      --dry-run         Show what would be done without executing
      --home string     Override home directory (for admin setup of other users)
      --verbose         Show library and debug logs on stderr
      --yes             Unattended mode (skip all prompts)
```

### SEE ALSO

* [dot peer](dot_peer.md)	 - Sync the workspace directly to another machine over SSH

