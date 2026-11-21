## dot peer takeover

Take the coordinator role while the other Mac is away

### Synopsis

Unplanned coordinator switch, run on the Mac that becomes active while
the current coordinator is unreachable:

  1. Validate the replica the last coordinator pushed after its last
     complete run: per-file sha256, generation not older than the last one
     this store saw, and a meta target that is the reverse of this profile.
  2. Show how the replica's filter files differ from the local ones and
     require confirmation.
  3. Install the replica baselines with the target markers rewritten for
     this profile, so delete provenance carries over from the last
     complete run. max_delete is never raised.
  4. Adopt this machine as owner with the next epoch and a pending fence.

While the fence is pending, peer setup and peer sync skip the reachability
and remote-owner requirements; an unreachable run still exits 0. When the
other Mac returns, the first run settles the role by epoch: this machine's
edits win simultaneous-edit conflicts, and the returning Mac adopts the new
owner, removes its scheduler and transfers nothing.

What the replica cannot cover is lost by design: unpushed commits and
uncommitted changes the old coordinator made after its last complete run.
After the switch, fetch and realign repos with `dot peer git realign --apply`.

```
dot peer takeover [flags]
```

### Examples

```
  dot peer takeover
```

### Options

```
  -h, --help[=true|false]   help for takeover
```

### Options inherited from parent commands

```
      --ascii[=true|false]     use ASCII markers regardless of locale
      --color string           color output: auto, always or never (default "auto")
      --config string          path to custom config YAML
      --dry-run[=true|false]   show what would be done without executing
      --home string            override home directory (for admin setup of other users)
      --verbose[=true|false]   show library and debug logs on stderr
      --yes[=true|false]       unattended mode (skip all prompts)
```

### SEE ALSO

* [dot peer](dot_peer.md)	 - Sync the workspace directly to another machine over SSH

