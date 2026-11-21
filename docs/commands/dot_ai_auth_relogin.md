## dot ai auth relogin

Clear stored credentials and re-authenticate MCP servers

```
dot ai auth relogin <server...> [flags]
```

### Examples

```
  dot ai auth relogin "claude.ai Canva"
```

### Options

```
  -h, --help[=true|false]         help for relogin
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

