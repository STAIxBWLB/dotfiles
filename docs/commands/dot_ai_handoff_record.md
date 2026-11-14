## dot ai handoff record

Record a selected agent's explicit summary and artifact fingerprints

```
dot ai handoff record [flags]
```

### Options

```
      --agent string           Selected agent ID producing the note
      --artifact stringArray   Repository-relative artifact to fingerprint (repeatable, at most 16, total hash budget 32 MiB)
  -h, --help                   help for record
      --kind string            Note kind: plan, progress, review, validation, learning (default "progress")
      --project string         Git repository or worktree directory (default ".")
      --result string          Producer claim: unverified, passed, failed (default "unverified")
      --summary-file string    UTF-8 curated summary file, at most 32 KiB
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

* [dot ai handoff](dot_ai_handoff.md)	 - Share explicit local development notes across selected agents

