## dot ai family rollback

Make a family tool's previous version current again

### Synopsis

Make the previous version of a family tool current again
(ahub, amem, vmem, acon, ares).

Running processes are listed and keep their files; new sessions start the
restored version. The version it leaves becomes 'previous'.

```
dot ai family rollback <id> [flags]
```

### Examples

```
  dot ai family rollback amem
```

### Options

```
  -h, --help[=true|false]   help for rollback
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

