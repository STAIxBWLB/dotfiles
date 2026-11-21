## dot status

Show full environment status at a glance

### Synopsis

Unified dashboard: system, user, modules, secrets, sync, and workspace projects.
The final summary counts enabled module checks; other sections are context.
This view exits 0 when it can produce the report, even when modules need
changes.

```
dot status [flags]
```

### Examples

```
  dot status
  dot status --profile server
```

### Options

```
  -h, --help[=true|false]   help for status
      --profile string      profile name (minimal, full, server)
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

* [dot](dot.md)	 - User environment & workspace management tool

