## dot ai vault

Retire the legacy Obsidian MCP integration

### Synopsis

Register vmem (@staix/vault-mem) as the agents' vault server for Kiro and
GitHub Copilot, and print the 'vmem setup' lines for Claude Code, Codex, Kimi,
Qwen, pi, jcode and Antigravity, which vmem registers itself.

dot runs no vault process and installs vmem only on request: install the exact
version yourself or with 'dot ai family install vmem', then run 'dot ai mcp install vmem'.

'dot ai vault retire-mcpvault' removes the generic Obsidian MCP server
("obsidian", @bitbonsai/mcpvault) from the configs dot owns and prints the
lines for the rest. It is a dry run unless --apply is given.

### Options

```
  -h, --help   help for vault
```

### Options inherited from parent commands

```
      --config string   Path to custom config YAML
      --dry-run         Show what would be done without executing
      --home string     Override home directory (for admin setup of other users)
      --yes             Unattended mode (skip all prompts)
```

### SEE ALSO

* [dot ai](dot_ai.md)	 - AI CLI/config helpers and settings backup/restore
* [dot ai vault retire-mcpvault](dot_ai_vault_retire-mcpvault.md)	 - Remove the Obsidian MCP server (dry run unless --apply)

