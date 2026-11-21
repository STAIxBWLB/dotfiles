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
      --adopt[=true|false]   repoint an existing ~/.local/bin/<bin> symlink, leaving the other install in place
  -h, --help[=true|false]    help for update
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

* [dot ai family](dot_ai_family.md)	 - Install agent-hub family tools side by side

