## dot ai family update

Move installed family tools to the registry's latest version

### Synopsis

Move each named family tool, or with no ids every installed one, to the
registry's latest version. A tool that is not installed is reported and
skipped, and one already newer than the registry's latest is left as is.
With --dry-run nothing is written: each tool is shown as already current or
with the version it would move to.

```
dot ai family update [id...] [flags]
```

### Examples

```
  dot ai family update
  dot ai family update ahub --dry-run
```

### Options

```
      --adopt   repoint an existing ~/.local/bin/<bin> symlink, leaving the other install in place
  -h, --help    help for update
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

* [dot ai family](dot_ai_family.md)	 - Install agent-hub family tools side by side

