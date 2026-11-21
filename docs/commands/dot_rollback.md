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

### Examples

```
  dot rollback
```

### Options

```
  -h, --help[=true|false]   help for rollback
      --wait duration       wait up to this long for the maintenance slot instead of deferring (e.g. 30m)
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

