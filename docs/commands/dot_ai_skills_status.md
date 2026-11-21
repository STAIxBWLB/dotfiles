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
  -h, --help[=true|false]   help for status
      --json[=true|false]   print JSON
      --provider string     skills SSOT provider: maru or path (defaults to maru)
      --ssot string         skills SSOT root path (defaults to ~/.maru/skills for provider=maru)
      --tool string         comma-separated managed targets (claude,codex); auto-detected when omitted
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

* [dot ai skills](dot_ai_skills.md)	 - Diagnose AI Markdown skills (read-only)

