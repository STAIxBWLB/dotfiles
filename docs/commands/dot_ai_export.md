## dot ai export

Export AI settings to a portable tar.gz archive

```
dot ai export <file.tar.gz> [flags]
```

### Examples

```
  dot ai export ai-settings.tar.gz
```

### Options

```
  -h, --help           help for export
      --include-auth   include auth/local-secret files
      --tag string     human-friendly label stored in meta.yaml
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

* [dot ai](dot_ai.md)	 - AI CLI/config helpers and settings backup/restore

