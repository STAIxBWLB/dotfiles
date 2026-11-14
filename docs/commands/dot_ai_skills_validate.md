## dot ai skills validate

Validate SKILL.md frontmatter and duplicate names

```
dot ai skills validate [flags]
```

### Options

```
  -h, --help               help for validate
      --json               Print JSON
      --root stringArray   Explicit skill root to scan; may be repeated and replaces default roots
      --strict             Treat legacy skills without schema_version/frontmatter as validation failures
      --tool string        Comma-separated default skill roots to scan (claude,codex,agents,gemini,antigravity)
```

### Options inherited from parent commands

```
      --color string    Color output: auto, always or never (default "auto")
      --config string   Path to custom config YAML
      --dry-run         Show what would be done without executing
      --home string     Override home directory (for admin setup of other users)
      --verbose         Show library and debug logs on stderr
      --yes             Unattended mode (skip all prompts)
```

### SEE ALSO

* [dot ai skills](dot_ai_skills.md)	 - Diagnose AI Markdown skills (read-only; the Maru app manages runtime symlinks)

