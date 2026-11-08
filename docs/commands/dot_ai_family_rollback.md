## dot ai family rollback

Make a family tool's previous version current again

### Synopsis

Make the previous version of a family tool (ahub, amem, vmem, acon, ares) current again.

Running processes are listed and keep their files; new sessions start the
restored version. The version it leaves becomes 'previous'.

```
dot ai family rollback <id> [flags]
```

### Options

```
  -h, --help   help for rollback
```

### Options inherited from parent commands

```
      --config string   Path to custom config YAML
      --dry-run         Show what would be done without executing
      --home string     Override home directory (for admin setup of other users)
      --yes             Unattended mode (skip all prompts)
```

### SEE ALSO

* [dot ai family](dot_ai_family.md)	 - Install agent-hub family tools side by side, one directory per version

