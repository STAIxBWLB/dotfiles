## dot peer doctor

Check that a peer sync would work before running one

### Synopsis

Probe everything that silently breaks a peer transfer.

Checks, and why each exists:
  local rsync    a non-login shell finds macOS openrsync before Homebrew's
                 3.x; openrsync escapes non-ASCII names in the inventory
  reachability   an offline peer must be a clean no-op, not a failure
  peer dot       the newest release among the peer's dot installs; a stale
                 build at ~/.local/bin/dot must not shadow it
  remote rsync   macOS 26 ships openrsync, which cannot receive -aHAX from a
                 3.x client — and --dry-run never surfaces it, because a dry
                 run ships no file data
  clock skew     "newer wins" is only meaningful if the clocks agree
  disk headroom  the receiving side has to hold the payload
  keychain       tokens there cannot be transferred, and cannot even be
                 verified over ssh — a reminder, not a failure

Then both machines are compared, each check with the command that fixes it
and the Mac to run it on: rsync on each side; names not in NFD, judged by
the sync's own rules (an unmarked coordinator's sync refuses its own, and
the other Mac's once a pull brings them; an NFD-marked one's diff and dry
run stop on the other Mac's); which machine is the coordinator, their owner
epochs and whether their profiles point at each other, as the sync's fence
decides it (a takeover's pending fence settles at the lower epoch's next
run); a scheduler only on the coordinator; a takeover replica on the other
Mac that a takeover would accept; the config only the coordinator's copy
applies (max_delete, propagation, filter files, host_merge,
include_submodules, filter_mode) where it differs, and a host_merge that
would stop the coordinator's sync. The doctor writes nothing on either Mac.

```
dot peer doctor [flags]
```

### Options

```
  -h, --help   help for doctor
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

