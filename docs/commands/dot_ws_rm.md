## dot ws rm

Remove a directory from both workspaces

```
dot ws rm <path> [flags]
```

### Examples

```
  dot ws rm work/dev/old
  dot ws rm work/dev/old --recursive
```

### Options

```
  -h, --help        help for rm
  -r, --recursive   remove non-empty directories
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

* [dot ws](dot_ws.md)	 - Dual-workspace (work + cloud mirror) folder operations

