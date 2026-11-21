## dot tunnel client add

Add an SSH ProxyCommand host block

```
dot tunnel client add <hostname> [flags]
```

### Examples

```
  dot tunnel client add mac.example.com
```

### Options

```
  -h, --help[=true|false]   help for add
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

* [dot tunnel client](dot_tunnel_client.md)	 - Manage SSH client config for Cloudflare Access

