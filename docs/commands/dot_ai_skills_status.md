## dot ai skills status

Show configured skills SSOT symlink status

```
dot ai skills status [flags]
```

### Examples

```
  dot ai skills status
  dot ai skills status --json
```

### Options

```
  -h, --help              help for status
      --json              print JSON
      --provider string   skills SSOT provider: maru or path (defaults to maru)
      --ssot string       skills SSOT root path (defaults to ~/.maru/skills for provider=maru)
      --tool string       comma-separated managed targets (claude,codex); auto-detected when omitted
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

* [dot ai skills](dot_ai_skills.md)	 - Diagnose AI Markdown skills (read-only)

