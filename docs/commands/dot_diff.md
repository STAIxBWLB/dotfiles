## dot diff

Show pending changes without applying

### Synopsis

Show what would change if 'dot apply' were run (dry-run with verbose change
details).

--summary lists each module's pending change descriptions without the
commands.

```
dot diff [flags]
```

### Examples

```
  dot diff
  dot diff --summary
```

### Options

```
  -h, --help             help for diff
      --module strings   run specific modules only
      --profile string   profile name (minimal, full, server)
      --summary          list pending changes per module without the commands
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

* [dot](dot.md)	 - User environment & workspace management tool

