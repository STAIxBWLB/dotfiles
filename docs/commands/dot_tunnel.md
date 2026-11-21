## dot tunnel

Manage Cloudflare Tunnel SSH access for this Mac

### Synopsis

Configure a locally managed Cloudflare Tunnel for SSH access to this Mac.

Server commands are macOS-only and install a system LaunchDaemon. Client
commands only manage ~/.ssh/config.d/dot-tunnel and work cross-platform.

```
dot tunnel [flags]
```

### Examples

```
  dot tunnel --help
```

### Options

```
  -h, --help[=true|false]   help for tunnel
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

* [dot](dot.md)	 - User environment & workspace management tool
* [dot tunnel client](dot_tunnel_client.md)	 - Manage SSH client config for Cloudflare Access
* [dot tunnel log](dot_tunnel_log.md)	 - Tail the Cloudflare Tunnel daemon error log
* [dot tunnel setup](dot_tunnel_setup.md)	 - Configure this Mac for SSH over Cloudflare Tunnel
* [dot tunnel status](dot_tunnel_status.md)	 - Show Cloudflare Tunnel daemon and SSH status
* [dot tunnel uninstall](dot_tunnel_uninstall.md)	 - Remove the dot-managed Cloudflare Tunnel daemon

