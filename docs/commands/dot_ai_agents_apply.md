## dot ai agents apply

Copy-render the SSOT to agent tool targets

```
dot ai agents apply [flags]
```

### Examples

```
  dot ai agents apply
  dot ai agents apply --tool claude,codex
```

### Options

```
      --force         overwrite a target changed outside the last dot-managed apply after backing it up
  -h, --help          help for apply
      --tool string   comma-separated tool IDs to apply
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

* [dot ai agents](dot_ai_agents.md)	 - Manage the shared AI agents instruction SSOT

