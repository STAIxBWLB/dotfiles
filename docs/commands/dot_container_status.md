## dot container status

Show the container backend, shim and DNS defaults

```
dot container status [flags]
```

### Examples

```
  dot container status
  dot container status --probe
```

### Options

```
  -h, --help[=true|false]    help for status
      --probe[=true|false]   also run container run --rm alpine nslookup example.com
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

* [dot container](dot_container.md)	 - Run the shared container backend

