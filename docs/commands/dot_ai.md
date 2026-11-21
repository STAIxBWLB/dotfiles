## dot ai

AI CLI/config helpers and settings backup/restore

### Synopsis

Manage portable AI assistant configuration.

The ai module writes shell/config helper files. It does not install Claude,
Codex, Antigravity, or ChatGPT apps; use 'dot apps install' for Homebrew casks.

With --include-auth, the snapshot carries each tool's credential file
(.codex/auth.json, .kimi-code/credentials, .pi/agent/auth.json,
.gemini/oauth_creds.json). pi sessions, extensions, and skills stay out:
they are machine state pi rebuilds itself.

```
dot ai [flags]
```

### Examples

```
  dot ai --help
```

### Options

```
  -h, --help[=true|false]   help for ai
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

* [dot](dot.md)	 - User environment & workspace management tool
* [dot ai agents](dot_ai_agents.md)	 - Manage the shared AI agents instruction SSOT
* [dot ai audit](dot_ai_audit.md)	 - Inspect append-only dot ai audit events
* [dot ai auth](dot_ai_auth.md)	 - Inspect and refresh OAuth credentials for MCP servers
* [dot ai backup](dot_ai_backup.md)	 - Snapshot only AI settings (dot backup is the front door)
* [dot ai coauthor-guard](dot_ai_coauthor-guard.md)	 - Warn or block AI-added Co-authored commit trailers
* [dot ai export](dot_ai_export.md)	 - Export AI settings to a portable tar.gz archive
* [dot ai family](dot_ai_family.md)	 - Install agent-hub family tools side by side
* [dot ai handoff](dot_ai_handoff.md)	 - Share local development notes across selected agents
* [dot ai hud](dot_ai_hud.md)	 - Manage dot-native Claude Code and Codex HUD status lines
* [dot ai import](dot_ai_import.md)	 - Import AI settings from a portable tar.gz archive
* [dot ai mcp](dot_ai_mcp.md)	 - Register and inspect amem, vmem and ares MCP integrations
* [dot ai memory](dot_ai_memory.md)	 - Audit and archive native agent memory
* [dot ai policy](dot_ai_policy.md)	 - Inspect, resolve and reconcile AI operating policies
* [dot ai prune](dot_ai_prune.md)	 - Delete older AI snapshots, keeping the newest N
* [dot ai restore](dot_ai_restore.md)	 - Restore only AI settings (dot restore is the front door)
* [dot ai session](dot_ai_session.md)	 - Launch controlled agents with adaptive policy
* [dot ai setup](dot_ai_setup.md)	 - Select agent CLIs, add-ons, instructions and skills
* [dot ai skills](dot_ai_skills.md)	 - Diagnose AI Markdown skills (read-only)
* [dot ai tools](dot_ai_tools.md)	 - Inspect or reconcile selected development tooling
* [dot ai update](dot_ai_update.md)	 - Update explicitly selected agent CLIs and development tools
* [dot ai vault](dot_ai_vault.md)	 - Register vmem as the agents' vault server

