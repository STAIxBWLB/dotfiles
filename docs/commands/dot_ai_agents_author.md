## dot ai agents author

Interactively or programmatically edit SSOT sections

```
dot ai agents author [flags]
```

### Examples

```
  dot ai agents author
  dot ai agents author --non-interactive --section style --value 'Be brief'
```

### Options

```
      --from-current string            pull from a live tool target before authoring
  -h, --help[=true|false]              help for author
      --non-interactive[=true|false]   update one section without the wizard
      --section string                 section name for --non-interactive
      --value string                   section value for --non-interactive
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

* [dot ai agents](dot_ai_agents.md)	 - Manage the shared AI agents instruction SSOT

