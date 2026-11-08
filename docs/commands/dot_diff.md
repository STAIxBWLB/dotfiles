## dot diff

Show pending changes without applying

### Synopsis

Show what would change if 'dot apply' were run (dry-run with verbose change details).

--summary lists each module's pending change descriptions without the
commands, the report 'dot check' printed before it was folded into diff.

```
dot diff [flags]
```

### Options

```
  -h, --help      help for diff
      --summary   List pending changes per module without the commands
```

### Options inherited from parent commands

```
      --config string    Path to custom config YAML
      --dry-run          Show what would be done without executing
      --home string      Override home directory (for admin setup of other users)
      --module strings   Run specific modules only
      --profile string   Profile name (minimal, full, server)
      --yes              Unattended mode (skip all prompts)
```

### SEE ALSO

* [dot](dot.md)	 - User environment & workspace management tool

