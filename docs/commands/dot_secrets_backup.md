## dot secrets backup

Copy only encrypted secrets

### Synopsis

Copy the encrypted *.age files from the local store to a destination.

With no destination, defaults to <backup-root>/secrets-age/<host> — the
same cloud root (Dropbox-preferred) the rest of dot backs up to.

```
dot secrets backup [destination] [flags]
```

### Examples

```
  dot secrets backup
  dot secrets backup ~/backups/secrets
```

### Options

```
  -h, --help[=true|false]   help for backup
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

