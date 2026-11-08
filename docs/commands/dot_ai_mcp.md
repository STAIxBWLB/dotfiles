## dot ai mcp

Register and inspect amem, vmem and ares MCP integrations

### Synopsis

Register an installed family tool's MCP integration or inspect its health.

Install the binary first with 'dot ai family install <id>'. Registration uses
only the selected agents and the files dot owns. Other agents still use the
printed native setup commands. No MCP service or LaunchAgent is started.
Memory audit/archive and legacy retirement stay under 'dot ai memory' and
'dot ai vault'.

```
dot ai mcp [flags]
```

### Options

```
  -h, --help   help for mcp
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
* [dot ai mcp install](dot_ai_mcp_install.md)	 - Register one installed family tool's MCP integration
* [dot ai mcp status](dot_ai_mcp_status.md)	 - Show one MCP integration's registration and version health

