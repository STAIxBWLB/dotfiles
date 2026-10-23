## dot ai memory install

Register amem's MCP server for the selected agents

### Synopsis

Add the amem MCP server ('amem mcp') to the config of each selected agent whose
MCP file dot owns, and print the lines for Claude Code and Codex. Every other
server and key in those files is preserved. Re-running changes nothing.

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

