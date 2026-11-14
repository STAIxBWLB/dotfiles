## dot tunnel

Manage Cloudflare Tunnel SSH access for this Mac

### Synopsis

Configure a locally managed Cloudflare Tunnel for SSH access to this Mac.

Server commands are macOS-only and install a system LaunchDaemon. Client
commands only manage ~/.ssh/config.d/dot-tunnel and work cross-platform.

```
dot tunnel [flags]
```

### Options

```
  -h, --help   help for tunnel
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

* [dot](dot.md)	 - User environment & workspace management tool
* [dot tunnel client](dot_tunnel_client.md)	 - Manage SSH client config for Cloudflare Access
* [dot tunnel log](dot_tunnel_log.md)	 - Tail the Cloudflare Tunnel daemon error log
* [dot tunnel setup](dot_tunnel_setup.md)	 - Configure this Mac for SSH over Cloudflare Tunnel
* [dot tunnel status](dot_tunnel_status.md)	 - Show Cloudflare Tunnel daemon and SSH status
* [dot tunnel uninstall](dot_tunnel_uninstall.md)	 - Remove the dot-managed Cloudflare Tunnel daemon

