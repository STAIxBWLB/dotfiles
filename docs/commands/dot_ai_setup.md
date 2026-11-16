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
      --ascii           use ASCII markers regardless of locale
      --color string    color output: auto, always or never (default "auto")
      --config string   path to custom config YAML
      --dry-run         show what would be done without executing
      --home string     override home directory (for admin setup of other users)
      --verbose         show library and debug logs on stderr
      --yes             unattended mode (skip all prompts)
```

### SEE ALSO

* [dot ai](dot_ai.md)	 - AI CLI/config helpers and settings backup/restore

