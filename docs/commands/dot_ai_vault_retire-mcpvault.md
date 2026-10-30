## dot ai vault retire-mcpvault

Remove the Obsidian MCP server (dry run unless --apply)

### Synopsis

Retire the generic Obsidian MCP server ("obsidian", @bitbonsai/mcpvault)
after vmem is registered.

Without --apply this lists every file it would change and changes nothing.
With --apply it removes mcpServers.obsidian from the Kiro and Copilot configs,
the files dot owns, for the selected agents only, and only from a config that
already registers a working vmem entry; any other is skipped and reported. It never edits Claude Code's or Codex's config: the exact
lines to run or delete are printed instead. It refuses while vmem is not on
PATH, so the vault is never left without an agent-facing server.

```
dot ai vault retire-mcpvault [flags]
```

### Options

```
      --apply   Remove the obsidian server from the files dot owns (default: dry run)
  -h, --help    help for retire-mcpvault
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

* [dot ai vault](dot_ai_vault.md)	 - Register vmem as the vault writer and retire the Obsidian MCP server

