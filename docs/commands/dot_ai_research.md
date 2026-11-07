## dot ai research

Register ares, the research engine, for the agents dot owns

### Synopsis

Register ares (@staix/agent-research) as the agents' research MCP server for
Kiro and GitHub Copilot, and print the 'ares setup' lines for Claude Code,
Codex, Kimi, Qwen, pi, jcode and Antigravity, which ares registers itself.

dot runs no research process and installs ares only on request: install it
with 'dot ai family install ares' (or 'bun add -g @staix/agent-research'),
then run 'dot ai research install'.

The AI policy auto-approves ares's read tools only. Tools that write a study,
decide a gate, reach the network or start or read a container keep their
approval prompt.

### Options

```
  -h, --help   help for research
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
* [dot ai research install](dot_ai_research_install.md)	 - Register ares's MCP server for Kiro and Copilot
* [dot ai research status](dot_ai_research_status.md)	 - Show ares integration health

