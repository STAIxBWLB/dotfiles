## dot register

Register a project for workspace management

### Synopsis

Register a directory as a named project. Defaults to current directory.

```
dot register <name> [path] [flags]
```

### Examples

```
  dot register myproject ~/workspace/work/myproject
```

### Options

```
  -h, --help[=true|false]   help for register
      --layout string       default layout for this project
      --theme string        default theme for this project
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

