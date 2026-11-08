## dot ai memory

Audit and archive native agent memory, or retire claude-mem

### Synopsis

Register amem (@staix/agent-mem) as the project memory for Kiro and
GitHub Copilot, and print the 'amem setup' lines for Claude Code, Codex, Kimi,
Qwen, pi and Antigravity, which amem registers itself.

dot runs no memory process and installs amem only on request: install the exact
version yourself or with 'dot ai family install amem', then run 'dot ai mcp install amem'. amem has no resident worker, so dot
writes no LaunchAgent for it. Sessions of agents without hooks reach amem
through 'amem ingest <agent>', run at session end or on demand.

'dot ai memory audit' reports the size and drift of each agent's native memory
store, and 'dot ai memory archive' moves stale files out of the loaded layer
(a dry run unless --apply is given).

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
* [dot ai memory archive](dot_ai_memory_archive.md)	 - Move stale memory files into an archive (dry run unless --apply)
* [dot ai memory audit](dot_ai_memory_audit.md)	 - Report the size and drift of each agent's native memory (read-only)
* [dot ai memory retire-claude-mem](dot_ai_memory_retire-claude-mem.md)	 - Remove the claude-mem integration (dry run unless --apply)

