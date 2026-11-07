## dot ai research install

Register ares's MCP server for Kiro and Copilot

### Synopsis

Add the ares MCP server ('ares mcp') to the Kiro and Copilot configs when
those agents are selected, write the research block into the agents SSOT
(after a backup), and print the 'ares setup' lines for the other agents. Every
other server and key is preserved. Re-running changes nothing.

ares must already be installed (an @staix/agent-research release inside the
range 'dot ai research status' names) and on PATH. With --dry-run nothing is
written and every file it would change is listed.

```
dot ai research install [flags]
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

* [dot ai research](dot_ai_research.md)	 - Register ares, the research engine, for the agents dot owns

