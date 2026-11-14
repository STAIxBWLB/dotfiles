## dot tunnel setup

Configure this Mac for SSH over Cloudflare Tunnel

```
dot tunnel setup [flags]
```

### Options

```
  -h, --help              help for setup
      --hostname string   public SSH hostname (required with --yes on first setup)
      --name string       tunnel name (default: dot-<short-hostname>)
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

