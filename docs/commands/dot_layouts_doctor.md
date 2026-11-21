## dot layouts doctor

Check the tools workspace layouts need

### Synopsis

Check required and optional workspace tools. The final summary counts their
results. Exit 1 when a required tool is missing; optional tools produce
warnings only.

```
dot layouts doctor [flags]
```

### Examples

```
  dot layouts doctor
```

### Options

```
  -h, --help[=true|false]   help for doctor
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

* [dot layouts](dot_layouts.md)	 - List available workspace layouts

