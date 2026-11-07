## dot ai family update

Move installed family tools to the registry's latest version

### Synopsis

Move each named family tool, or with no ids every installed one, to the
registry's latest version. A tool that is not installed is reported and
skipped, and one already newer than the registry's latest is left as is. With --dry-run nothing is written: each tool is shown as already
current or with the version it would move to.

```
dot ai family update [id...] [flags]
```

### Options

```
      --adopt   Repoint an existing ~/.local/bin/<bin> symlink from another install; that install is left in place
  -h, --help    help for update
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

* [dot ai family](dot_ai_family.md)	 - Install agent-hub family tools side by side, one directory per version

