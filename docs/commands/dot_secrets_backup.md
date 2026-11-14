## dot secrets backup

Advanced: copy only encrypted secrets (dot backup is the front door)

### Synopsis

Copy the encrypted *.age files from the local store to a destination.

With no destination, defaults to <backup-root>/secrets-age/<host> — the
same cloud root (Dropbox-preferred) the rest of dot backs up to.

```
dot secrets backup [destination] [flags]
```

### Options

```
  -h, --help   help for backup
```

### Options inherited from parent commands

```
      --ascii           Use ASCII markers regardless of locale
      --color string    Color output: auto, always or never (default "auto")
      --config string   Path to custom config YAML
      --dry-run         Show what would be done without executing
      --home string     Override home directory (for admin setup of other users)
      --verbose         Show library and debug logs on stderr
      --yes             Unattended mode (skip all prompts)
```

### SEE ALSO

* [dot secrets](dot_secrets.md)	 - Manage encrypted secrets

