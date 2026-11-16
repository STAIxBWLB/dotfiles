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

### Examples

```
  dot ai mcp --help
```

### Options

```
  -h, --help   help for mcp
```

### Options inherited from parent commands

```
      --ascii           use ASCII markers regardless of locale
      --color string    color output: auto, always or never (default "auto")
      --config string   path to custom config YAML
      --dry-run         show what would be done without executing
      --home string     override home directory (for admin setup of other users)
      --verbose         show library and debug logs on stderr
      --yes             unattended mode (skip all prompts)
```

### SEE ALSO

* [dot ai](dot_ai.md)	 - AI CLI/config helpers and settings backup/restore
* [dot ai mcp install](dot_ai_mcp_install.md)	 - Register one installed family tool's MCP integration
* [dot ai mcp status](dot_ai_mcp_status.md)	 - Show one MCP integration's registration and version health

