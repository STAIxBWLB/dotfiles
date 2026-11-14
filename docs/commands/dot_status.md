## dot status

Show full environment status at a glance

### Synopsis

Unified dashboard: system, user, modules, secrets, sync, and workspace projects.
The final summary counts enabled module checks; other sections are context.
This view exits 0 when it can produce the report, even when modules need changes.

```
dot status [flags]
```

### Options

```
  -h, --help             help for status
      --profile string   Profile name (minimal, full, server)
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

* [dot](dot.md)	 - User environment & workspace management tool

