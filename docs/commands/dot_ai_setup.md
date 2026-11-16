## dot ai setup

Select agent CLIs, add-ons, instructions and skills

```
dot ai setup [flags]
```

### Examples

```
  dot ai setup
  dot ai setup --non-interactive --agents claude,codex
```

### Options

```
      --agents string     selected agent IDs, comma separated (empty selects none)
  -h, --help              help for setup
      --non-interactive   require explicit or saved selections without prompting
      --skills string     selected Maru skill names or IDs, comma separated
      --tools string      selected add-on IDs: ripwire,ocr,gsd,ponytail
```

### Options inherited from parent commands

```
      --ascii           Use ASCII markers regardless of locale
      --color string    Color output: auto, always or never (default "auto")
      --config string   Path to custom config YAML
      --dry-run         Show what would be done without executing
      --home string     Override home directory (for admin setup of other users)
      --verbose         Show library and debug logs on stderr
      --yes             Unattended mode (skip all prompts)
```

### SEE ALSO

* [dot ai](dot_ai.md)	 - AI CLI/config helpers and settings backup/restore

