## dot ai auth

Inspect and refresh OAuth credentials for MCP servers

### Synopsis

Manage MCP server authentication.

OAuth-backed MCP servers (Cloudflare plugin servers, claude.ai connectors)
periodically lose their credentials. 'status' reports which servers need
re-auth, 'login' authenticates them, and 'relogin' clears stale credentials
before logging in again.

Server names containing spaces must be quoted.

### Examples

```
  dot ai auth status
  dot ai auth relogin "claude.ai Canva"
```

### Options

```
  -h, --help[=true|false]   help for auth
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

* [dot ai](dot_ai.md)	 - AI CLI/config helpers and settings backup/restore
* [dot ai auth login](dot_ai_auth_login.md)	 - Authenticate one or more MCP servers
* [dot ai auth relogin](dot_ai_auth_relogin.md)	 - Clear stored credentials and re-authenticate MCP servers
* [dot ai auth status](dot_ai_auth_status.md)	 - Show MCP servers and which ones need re-authentication

