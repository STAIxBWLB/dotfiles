## dot diff

Show pending changes without applying

### Synopsis

Show what would change if 'dot apply' were run (dry-run with verbose change details).

--summary lists each module's pending change descriptions without the
commands.

```
dot diff [flags]
```

### Options

```
  -h, --help             help for diff
      --module strings   Run specific modules only
      --profile string   Profile name (minimal, full, server)
      --summary          List pending changes per module without the commands
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

