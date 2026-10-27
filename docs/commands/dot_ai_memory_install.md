## dot ai memory install

Register amem's MCP server for Kiro and Copilot

### Synopsis

Add the amem MCP server ('amem mcp') to the Kiro and Copilot configs when
those agents are selected, and print the 'amem setup' lines for the other
agents. dot leaves the Kimi, Qwen and Antigravity configs untouched: 'amem setup'
owns them. Every other server and key is preserved. Re-running changes nothing.

amem must already be installed (the exact @staix/agent-mem version, which
'dot ai memory status' names) and on PATH. With --dry-run nothing is written.

```
dot ai memory install [flags]
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

* [dot ai memory](dot_ai_memory.md)	 - Register amem for the agents and retire claude-mem

