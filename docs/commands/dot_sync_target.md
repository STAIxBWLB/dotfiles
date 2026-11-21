## dot sync target

Show or set the sync target (local mirror dir or SSH remote)

### Synopsis

With no argument, prints the resolved sync target.

With a spec, sets the target in this workspace's local config
(<workspace>/.dotfiles/sync/config.yaml) so it takes effect immediately.
Accepted forms:

  local:~/Dropbox/work       local directory (a cloud client's folder)
  ssh:user@host:~/work       rsync over SSH
  ~/Dropbox/work             bare path — shorthand for local:

The target belongs to this workspace: it is written only to that store's
config.yaml, and another workspace keeps its own. A workspace with no
target of its own syncs to the detected cloud folder.

```
dot sync target [spec] [flags]
```

### Examples

```
  dot sync target
  dot sync target local:~/Dropbox/work
```

### Options

```
  -h, --help[=true|false]   help for target
```

### Options inherited from parent commands

```
      --ascii[=true|false]     use ASCII markers regardless of locale
      --color string           color output: auto, always or never (default "auto")
      --config string          path to custom config YAML
      --dry-run[=true|false]   show what would be done without executing
      --home string            override home directory (for admin setup of other users)
      --store string           sync store under <workspace>/.dotfiles/<store>/; "sync" is the cloud mirror, "peer" the other Mac (default "sync")
  -V, --verbose[=true|false]   show rsync progress output
      --yes[=true|false]       unattended mode (skip all prompts)
```

### SEE ALSO

* [dot sync](dot_sync.md)	 - Sync workspace to a local mirror or SSH remote via rsync

