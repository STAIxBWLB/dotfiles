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
      --all-needed[=true|false]   log in to every server currently flagged as needing auth
  -h, --help[=true|false]         help for login
      --no-browser[=true|false]   print the authorization URL instead of opening a browser
      --tool string               the CLI owning the MCP server (claude or codex) (default "claude")
```

### Options inherited from parent commands

```
      --ascii[=true|false]     use ASCII markers regardless of locale
      --color string           color output: auto, always or never (default "auto")
      --config string          path to custom config YAML
      --dry-run[=true|false]   show what would be done without executing
      --home string            override home directory (for admin setup of other users)
      --verbose[=true|false]   show library and debug logs on stderr
      --yes[=true|false]       unattended mode (skip all prompts)
```

### SEE ALSO

* [dot ai auth](dot_ai_auth.md)	 - Inspect and refresh OAuth credentials for MCP servers

