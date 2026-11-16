## dot secrets init

Encrypt SSH key and shell secrets with age

```
dot secrets init [flags]
```

### Examples

```
  dot secrets init --scaffold
```

### Options

```
  -h, --help       help for init
      --scaffold   create empty ~/.config/shell/90-secrets.sh template (0600) if missing
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

* [dot secrets](dot_secrets.md)	 - Manage encrypted secrets

