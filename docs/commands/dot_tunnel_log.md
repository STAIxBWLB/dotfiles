## dot tunnel log

Tail the Cloudflare Tunnel daemon error log

```
dot tunnel log [N] [flags]
```

### Examples

```
  dot tunnel log
  dot tunnel log 50
```

### Options

```
  -h, --help   help for log
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

* [dot tunnel](dot_tunnel.md)	 - Manage Cloudflare Tunnel SSH access for this Mac

