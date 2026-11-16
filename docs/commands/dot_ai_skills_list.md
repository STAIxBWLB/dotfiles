## dot ai skills list

List SKILL.md files and metadata status

```
dot ai skills list [flags]
```

### Examples

```
  dot ai skills list
  dot ai skills list --tool claude --json
```

### Options

```
  -h, --help               help for list
      --json               print JSON
      --root stringArray   explicit skill root to scan; may be repeated and replaces default roots
      --strict             treat legacy skills without schema_version/frontmatter as validation failures
      --tool string        comma-separated default skill roots to scan (claude,codex,agents,gemini,antigravity)
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

