## dot ws audit

Report structural mismatches between workspaces

### Synopsis

Read-only structure report. Mismatches are warnings in the final summary.
Exit 0 when the report can be read, even when the trees differ.

```
dot ws audit [scope] [flags]
```

### Examples

```
  dot ws audit
  dot ws audit work
```

### Options

```
  -h, --help   help for audit
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

* [dot ws](dot_ws.md)	 - Dual-workspace (work + cloud mirror) folder operations

