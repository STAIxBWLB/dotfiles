## dot ai auth login

Authenticate one or more MCP servers

```
dot ai auth login [server...] [flags]
```

### Examples

```
  dot ai auth login --all-needed
  dot ai auth login "claude.ai Canva"
```

### Options

```
      --all-needed    log in to every server currently flagged as needing auth
  -h, --help          help for login
      --no-browser    print the authorization URL instead of opening a browser
      --tool string   the CLI owning the MCP server (claude or codex) (default "claude")
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

* [dot ai auth](dot_ai_auth.md)	 - Inspect and refresh OAuth credentials for MCP servers

