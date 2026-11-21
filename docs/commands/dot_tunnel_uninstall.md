## dot tunnel uninstall

Remove the dot-managed Cloudflare Tunnel daemon

### Synopsis

Remove the dot-managed Cloudflare Tunnel LaunchDaemon.

Removing /etc/cloudflared config/credentials and deleting the Cloudflare
tunnel itself are interactive-only prompts that default to No; --yes never
auto-confirms them.

```
dot tunnel uninstall [flags]
```

### Examples

```
  dot tunnel uninstall
```

### Options

```
  -h, --help[=true|false]   help for uninstall
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

* [dot tunnel](dot_tunnel.md)	 - Manage Cloudflare Tunnel SSH access for this Mac

