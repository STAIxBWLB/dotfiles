## dot ai skills validate

Validate SKILL.md frontmatter and duplicate names

```
dot ai skills validate [flags]
```

### Examples

```
  dot ai skills validate
  dot ai skills validate --strict
```

### Options

```
  -h, --help               help for validate
      --json               print JSON
      --root stringArray   explicit skill root to scan; may be repeated and replaces default roots
      --strict             treat legacy skills without schema_version/frontmatter as validation failures
      --tool string        comma-separated default skill roots to scan (claude,codex,agents,gemini,antigravity)
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

* [dot ai skills](dot_ai_skills.md)	 - Diagnose AI Markdown skills (read-only)

