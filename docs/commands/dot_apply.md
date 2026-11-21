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
  -h, --help[=true|false]   help for apply
      --module strings      run specific modules only
      --profile string      profile name (minimal, full, server)
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

