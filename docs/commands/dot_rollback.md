## dot rollback

Activate the previous standalone dot binary

### Synopsis

Restore the previous standalone dot binary without downloading.

Versions live under ~/.local/share/dotfiles/tools/dot/; current and previous
are switched by atomic link renames. Existing guard hooks keep their stable
path. The version being left becomes previous, so another rollback restores it.
Homebrew installs must manage versions through brew instead.

```
dot rollback [flags]
```

### Options

```
  -h, --help            help for rollback
      --wait duration   wait up to this long for the maintenance slot instead of deferring (e.g. 30m)
```

### Options inherited from parent commands

```
      --color string    Color output: auto, always or never (default "auto")
      --config string   Path to custom config YAML
      --dry-run         Show what would be done without executing
      --home string     Override home directory (for admin setup of other users)
      --verbose         Show library and debug logs on stderr
      --yes             Unattended mode (skip all prompts)
```

### SEE ALSO

* [dot](dot.md)	 - User environment & workspace management tool

