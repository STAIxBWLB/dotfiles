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
  -h, --help    help for status
      --probe   also run container run --rm alpine nslookup example.com
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

* [dot container](dot_container.md)	 - Run the shared container backend

