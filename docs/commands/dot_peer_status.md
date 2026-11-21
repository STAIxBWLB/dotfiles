## dot peer status

Show local peer profile and scheduler status

```
dot peer status [flags]
```

### Examples

```
  dot peer status
  dot peer status --json
```

### Options

```
  -h, --help[=true|false]   help for status
      --json[=true|false]   print a stable machine-readable status document
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

