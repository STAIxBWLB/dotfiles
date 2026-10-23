## dot ai memory retire-claude-mem

Remove the claude-mem integration (dry run unless --apply)

### Synopsis

Retire claude-mem after its notes were imported into amem.

Without --apply this only lists what would be touched and reads back what is
left on the machine; it changes nothing. With --apply, in order, it:

  1. disables and uninstalls the claude-mem plugin for Claude Code and Codex,
     and removes their claude-mem marketplaces (through the native CLIs);
  2. removes the claude-mem entry from the MCP configs of Kimi, Qwen,
     Antigravity, Kiro and Copilot;
  3. boots out and removes the two dotfiles LaunchAgents;
  4. moves ~/.claude-mem to ~/.claude-mem.archive-<date> (never deletes it,
     and refuses while a claude-mem process runs);
  5. reads back that no claude-mem process, port, hook or LaunchAgent remains.

It never edits ~/.claude/settings.json, which Claude Code owns: the exact lines
to remove are printed for you. --apply refuses unless --import-complete says
the 'amem import claude-mem' run is done and an amem import cursor exists.

```
dot ai memory retire-claude-mem [flags]
```

### Options

```
      --apply                                    Run the retirement (default: dry run)
  -h, --help                                     help for retire-claude-mem
      --import-complete amem import claude-mem   Confirm that amem import claude-mem has finished
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

* [dot ai memory](dot_ai_memory.md)	 - Register amem for the agents and retire claude-mem

