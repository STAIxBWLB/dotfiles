## dot tunnel setup

Configure this Mac for SSH over Cloudflare Tunnel

```
dot tunnel setup [flags]
```

### Examples

```
  dot tunnel setup --hostname mac.example.com
```

### Options

```
  -h, --help              help for setup
      --hostname string   public SSH hostname (required with --yes on first setup)
      --name string       tunnel name (default: dot-<short-hostname>)
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

