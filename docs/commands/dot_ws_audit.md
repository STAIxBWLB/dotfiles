## dot ws audit

Report structural mismatches between workspaces

### Synopsis

Read-only structure report. Mismatches are warnings in the final summary.
Exit 0 when the report can be read, even when the trees differ.

```
dot ws audit [scope] [flags]
```

### Options

```
  -h, --help   help for audit
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

* [dot ws](dot_ws.md)	 - Dual-workspace (work + cloud mirror) folder operations

