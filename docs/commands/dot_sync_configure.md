## dot sync configure

Set target, owner and scheduler settings in one call

```
dot sync configure [flags]
```

### Examples

```
  dot sync configure --target local:~/Dropbox/work
  dot sync configure --propagate create,update --json
```

### Options

```
      --filter-mode string     filter mode to save: include or exclude
  -h, --help[=true|false]      help for configure
      --json[=true|false]      print the refreshed status document
      --max-delete int         maximum deletes per push; 0 uses the default (default -1)
      --owner string           self, none, or a machine name
      --propagate string       comma-separated create,update,delete
      --pull-interval string   pull cadence, or 0 to disable
      --pull-mode string       clean or force
      --push-interval string   push cadence, or 0 to disable
      --push-mode string       clean or force
      --target string          local:path or ssh:user@host:path
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

