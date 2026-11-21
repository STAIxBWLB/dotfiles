## dot ai audit tail

Print the last N dot ai audit events as JSONL

```
dot ai audit tail [<count>] [flags]
```

### Examples

```
  dot ai audit tail
  dot ai audit tail 50
```

### Options

```
  -h, --help[=true|false]   help for tail
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

* [dot ai audit](dot_ai_audit.md)	 - Inspect append-only dot ai audit events

