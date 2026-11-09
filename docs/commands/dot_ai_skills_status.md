## dot ai skills status

Show configured skills SSOT symlink status

```
dot ai skills status [flags]
```

### Options

```
  -h, --help              help for status
      --json              Print JSON
      --provider string   Skills SSOT provider: maru or path (defaults to maru)
      --ssot string       Skills SSOT root path (defaults to ~/.maru/skills for provider=maru)
      --tool string       Comma-separated managed targets (claude,codex); auto-detected when omitted
```

### Options inherited from parent commands

```
      --config string   Path to custom config YAML
      --dry-run         Show what would be done without executing
      --home string     Override home directory (for admin setup of other users)
      --yes             Unattended mode (skip all prompts)
```

### SEE ALSO

* [dot ai skills](dot_ai_skills.md)	 - Diagnose AI Markdown skills (read-only; the Maru app manages runtime symlinks)

