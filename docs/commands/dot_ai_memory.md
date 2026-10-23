## dot ai memory

Register amem for the agents and retire claude-mem

### Synopsis

Register amem (@staix/agent-mem) as the project memory for Kimi, Qwen,
Antigravity, Kiro and GitHub Copilot, and print the exact lines for Claude Code
and Codex, which keep their own plugin and MCP state.

dot never installs amem and runs no memory process: install the exact version
yourself, then run 'dot ai memory install'. amem has no resident worker, so dot
writes no LaunchAgent for it. Sessions of agents without hooks reach amem
through 'amem ingest <agent>', run at session end or on demand.

'dot ai memory retire-claude-mem' removes the old claude-mem integration. It is
a dry run unless --apply is given.

### Options

```
  -h, --help   help for memory
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
* [dot ai memory install](dot_ai_memory_install.md)	 - Register amem's MCP server for the selected agents
* [dot ai memory retire-claude-mem](dot_ai_memory_retire-claude-mem.md)	 - Remove the claude-mem integration (dry run unless --apply)
* [dot ai memory status](dot_ai_memory_status.md)	 - Show amem integration health

