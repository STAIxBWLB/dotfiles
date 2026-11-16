## dot peer diff

List paths where this machine and the peer disagree

### Synopsis

Count where the two machines disagree. --list prints every planned action,
workspace and host paths alike, with each side's size and mtime; --json
prints the same plan as a document. The deletion counts are shown next to
max_delete, which caps each direction of the workspace and of the tracked
host paths.

```
dot peer diff [flags]
```

### Examples

```
  dot peer diff
  dot peer diff --list
```

### Options

```
  -h, --help   help for diff
      --json   print the itemized plan as JSON
      --list   print every planned action, host paths included
```

### Options inherited from parent commands

```
      --ascii           use ASCII markers regardless of locale
      --color string    color output: auto, always or never (default "auto")
      --config string   path to custom config YAML
      --dry-run         show what would be done without executing
      --home string     override home directory (for admin setup of other users)
      --verbose         show library and debug logs on stderr
      --yes             unattended mode (skip all prompts)
```

### SEE ALSO

* [dot peer](dot_peer.md)	 - Sync the workspace directly to another machine over SSH

