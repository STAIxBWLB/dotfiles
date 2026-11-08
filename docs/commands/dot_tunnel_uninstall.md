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
      --config string   Path to custom config YAML
      --dry-run         Show what would be done without executing
      --home string     Override home directory (for admin setup of other users)
      --yes             Unattended mode (skip all prompts)
```

### SEE ALSO

* [dot tunnel](dot_tunnel.md)	 - Manage Cloudflare Tunnel SSH access for this Mac

