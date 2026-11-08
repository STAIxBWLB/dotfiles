## dot clean

Remove junk directories (node_modules, caches, venvs) from workspace

### Synopsis

Scan for and remove directories that waste disk space and cause
cloud sync problems: node_modules, __pycache__, .venv,
build caches, and .DS_Store files.

Default: scan and show what would be removed (preview mode).
Use --yes to actually delete. Use --all to include risky patterns
(dist/, build/, out/, target/).

The _sys/ subtree is ALWAYS protected and will never be touched.

```
dot clean [path] [flags]
```

### Options

```
      --all    Include risky patterns (dist/, build/, out/, target/)
  -h, --help   help for clean
```

### Options inherited from parent commands

```
      --config string   Path to custom config YAML
      --dry-run         Show what would be done without executing
      --home string     Override home directory (for admin setup of other users)
      --yes             Unattended mode (skip all prompts)
```

### SEE ALSO

* [dot](dot.md)	 - User environment & workspace management tool

