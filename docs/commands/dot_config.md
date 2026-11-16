## dot config

Show current configuration

### Synopsis

Display active profile, system info, enabled modules, and user settings.

```
dot config [flags]
```

### Examples

```
  dot config
  dot config --profile server
```

### Options

```
  -h, --help             help for config
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
* [dot config export](dot_config_export.md)	 - Export configuration to a portable YAML file

