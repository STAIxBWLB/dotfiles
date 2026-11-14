## dot ai auth relogin

Clear stored credentials and re-authenticate MCP servers

```
dot ai auth relogin <server...> [flags]
```

### Options

```
  -h, --help          help for relogin
      --no-browser    Print the authorization URL instead of opening a browser
      --tool string   CLI owning the MCP server (claude or codex) (default "claude")
```

### Options inherited from parent commands

```
      --ascii           Use ASCII markers regardless of locale
      --color string    Color output: auto, always or never (default "auto")
      --config string   Path to custom config YAML
      --dry-run         Show what would be done without executing
      --home string     Override home directory (for admin setup of other users)
      --verbose         Show library and debug logs on stderr
      --yes             Unattended mode (skip all prompts)
```

### SEE ALSO

* [dot ai auth](dot_ai_auth.md)	 - Inspect and refresh OAuth credentials for MCP servers

