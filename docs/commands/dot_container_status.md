## dot container status

Show the container backend, shim, DNS defaults and legacy snippet

```
dot container status [flags]
```

### Options

```
  -h, --help    help for status
      --probe   Also run container run --rm alpine nslookup example.com
```

### Options inherited from parent commands

```
      --config string    Path to custom config YAML
      --dry-run          Show what would be done without executing
      --home string      Override home directory (for admin setup of other users)
      --profile string   Profile name (minimal, full, server)
      --yes              Unattended mode (skip all prompts)
```

### SEE ALSO

* [dot container](dot_container.md)	 - Shared container command (apple/container on macOS, docker or podman on Linux)

