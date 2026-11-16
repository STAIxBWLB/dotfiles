## dot ai handoff record

Record an agent's summary and artifact fingerprints

```
dot ai handoff record [flags]
```

### Examples

```
  dot ai handoff record --agent codex --kind review --summary-file notes.md
```

### Options

```
      --agent string           selected agent ID producing the note
      --artifact stringArray   repository-relative artifact to fingerprint (repeatable, at most 16, total hash budget 32 MiB)
  -h, --help                   help for record
      --kind string            note kind: plan, progress, review, validation, learning (default "progress")
      --project string         git repository or worktree directory (default ".")
      --result string          producer claim: unverified, passed, failed (default "unverified")
      --summary-file string    curated UTF-8 summary file, at most 32 KiB
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

* [dot ai handoff](dot_ai_handoff.md)	 - Share local development notes across selected agents

