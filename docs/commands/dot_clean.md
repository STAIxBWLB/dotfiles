## dot clean

Remove junk directories from the workspace

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

### Examples

```
  dot clean
  dot clean ~/workspace --yes
```

### Options

```
      --all    include risky patterns (dist/, build/, out/, target/)
  -h, --help   help for clean
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

