## dot ai vault

Register vmem as the vault writer and retire the Obsidian MCP server

### Synopsis

Register vmem (@staix/vault-mem) as the agents' vault server for Kiro and
GitHub Copilot, and print the 'vmem setup' lines for Claude Code, Codex, Kimi,
Qwen, pi, jcode and Antigravity, which vmem registers itself.

dot never installs vmem and runs no vault process: install the exact version
yourself, then run 'dot ai vault install'.

'dot ai vault retire-mcpvault' removes the generic Obsidian MCP server
("obsidian", @bitbonsai/mcpvault) from the configs dot owns and prints the
lines for the rest. It is a dry run unless --apply is given.

### Options

```
  -h, --help   help for vault
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
* [dot ai vault install](dot_ai_vault_install.md)	 - Register vmem's MCP server for Kiro and Copilot
* [dot ai vault retire-mcpvault](dot_ai_vault_retire-mcpvault.md)	 - Remove the Obsidian MCP server (dry run unless --apply)
* [dot ai vault status](dot_ai_vault_status.md)	 - Show vmem integration health

