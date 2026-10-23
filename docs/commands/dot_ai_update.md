## dot ai update

Update explicitly selected agent CLIs and development tools

### Synopsis

Update only the saved nine-agent/four-addon allowlist.

Run 'dot ai setup' first. Native providers retain installation ownership.
Heavy work is serialized by the host resource guard. Unavailable metadata,
unsupported adapters, resource pressure, authentication and trust remain
explicitly pending; no blanket plugin, marketplace, or skill update runs.

```
dot ai update [flags]
```

### Options

```
      --check          Inspect installed and available versions without mutation
  -h, --help           help for update
      --json           Emit machine-readable JSON
      --tool strings   Limit to saved selections (claude,codex,kimi,qwen,grok,opencode,gencode,pi,antigravity,ripwire,ocr,gsd,ponytail)
```

### Options inherited from parent commands

```
      --config string    Path to custom config YAML
      --dry-run          Show what would be done without executing
      --home string      Override home directory (for admin setup of other users)
      --module strings   Run specific modules only
      --profile string   Profile name (minimal, full, server)
      --yes              Unattended mode (skip all prompts)
```

### SEE ALSO

* [dot ai](dot_ai.md)	 - AI CLI/config helpers and settings backup/restore
* [dot ai update schedule](dot_ai_update_schedule.md)	 - Manage opt-in Sunday 04:00 stable maintenance (macOS)

