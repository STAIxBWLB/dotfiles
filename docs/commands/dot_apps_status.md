## dot apps status

Show install + backup presence for tracked apps

```
dot apps status [flags]
```

### Examples

```
  dot apps status
```

### Options

```
      --from string         backup root to inspect (overrides configured BackupDir)
  -h, --help[=true|false]   help for status
      --host string         hostname to inspect (default: this host)
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

* [dot apps](dot_apps.md)	 - Install macOS apps and back up their settings

