## dot apply

Apply dot configuration

### Synopsis

Apply the selected profile's configuration to the user environment.

```
dot apply [flags]
```

### Examples

```
  dot apply
  dot apply --profile minimal --dry-run
```

### Options

```
  -h, --help             help for apply
      --module strings   run specific modules only
      --profile string   profile name (minimal, full, server)
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

* [dot](dot.md)	 - User environment & workspace management tool

