## dot secrets restore

Decrypt only secrets

```
dot secrets restore <source> [flags]
```

### Examples

```
  dot secrets restore ~/backups/secrets
```

### Options

```
  -h, --help[=true|false]   help for restore
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

* [dot secrets](dot_secrets.md)	 - Manage encrypted secrets

