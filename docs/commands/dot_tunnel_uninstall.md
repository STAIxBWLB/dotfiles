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

### Options

```
  -h, --help   help for uninstall
```

### Options inherited from parent commands

```
      --color string    Color output: auto, always or never (default "auto")
      --config string   Path to custom config YAML
      --dry-run         Show what would be done without executing
      --home string     Override home directory (for admin setup of other users)
      --verbose         Show library and debug logs on stderr
      --yes             Unattended mode (skip all prompts)
```

### SEE ALSO

* [dot tunnel](dot_tunnel.md)	 - Manage Cloudflare Tunnel SSH access for this Mac

