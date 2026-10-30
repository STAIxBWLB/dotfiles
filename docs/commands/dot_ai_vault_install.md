## dot ai vault install

Register vmem's MCP server for Kiro and Copilot

### Synopsis

Add the vmem MCP server ('vmem mcp') to the Kiro and Copilot configs when
those agents are selected, write the vault block into the agents SSOT (after a
backup), and print the 'vmem setup' lines for the other agents. Every other
server and key is preserved. Re-running changes nothing.

vmem must already be installed (the exact @staix/vault-mem version, which
'dot ai vault status' names) and on PATH. With --dry-run nothing is written.

```
dot ai vault install [flags]
```

### Options

```
  -h, --help   help for install
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

* [dot ai vault](dot_ai_vault.md)	 - Register vmem as the vault writer and retire the Obsidian MCP server

