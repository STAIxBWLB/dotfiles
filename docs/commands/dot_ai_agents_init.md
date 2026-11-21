## dot ai agents init

Create the shared agents SSOT

```
dot ai agents init [flags]
```

### Examples

```
  dot ai agents init
  dot ai agents init --from-current claude
```

### Options

```
      --force[=true|false]    overwrite an existing SSOT after backing it up
      --from-current string   seed AGENTS.md from an existing tool target
  -h, --help[=true|false]     help for init
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

