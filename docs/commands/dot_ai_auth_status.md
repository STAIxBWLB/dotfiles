## dot ai auth status

Show MCP servers and which ones need re-authentication

### Synopsis

List configured MCP servers and flag the ones needing re-authentication.

For claude, the pending set is read from Claude Code's own cache
(~/.claude/mcp-needs-auth-cache.json), so it reflects the last state Claude
recorded: a server re-authenticated outside dot can still appear as pending
until Claude rewrites the file. Pass --probe for live state.

For codex (--tool codex), servers come from 'codex mcp list --json'. Codex
keeps MCP OAuth in the macOS Keychain keyed by server config hash and exposes
no stale-credential state, so the oauth column only classifies the transport;
--probe streams 'codex doctor'.

```
dot ai auth status [flags]
```

### Examples

```
  dot ai auth status
  dot ai auth status --tool codex --json
```

### Options

```
  -h, --help          help for status
      --json          emit machine-readable JSON
      --probe         also stream live state ('claude mcp list' / 'codex doctor')
      --tool string   the CLI owning the MCP servers (claude or codex) (default "claude")
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

