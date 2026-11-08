## dot ai memory audit

Report the size and drift of each agent's native memory (read-only)

### Synopsis

Read the native memory of Claude Code, Codex, Qwen, Grok and jcode and report,
per store: file count and bytes, the always-loaded index against its limit
(Claude MEMORY.md: 200 lines or 25 KB; Codex memory_summary.md: 10 KB), index
links to missing files, memory files no index links to, hits of retired terms,
and the last change. A table shows each retired term's hits per agent.

Stores read: Claude projects/*/memory under $CLAUDE_CONFIG_DIR and ~/.claude;
$CODEX_HOME/memories (else ~/.codex/memories); ~/.qwen/memories and
~/.qwen/projects/*/memory; the MEMORY.md indexes under ~/.grok/memory-v2; and
the counts of 'jcode memory stats'. A store that is missing is reported as
unavailable, never as clean.

Retired terms come from ~/.config/dotfiles/agents/memory-markers.yaml:

  markers:
    - pattern: old-plugin-name        # case-insensitive text
      retired: 2026-10-01
      replacement: new-tool
      reason: the plugin was removed
    - pattern: 'old-repo(-v1)?'
      regex: true

The audit writes nothing. Output holds counts and file names only; --show also
prints the matching lines. Exit code 1 when an index is over its limit or links
a missing file.

```
dot ai memory audit [flags]
```

### Options

```
      --agent strings   Agents to audit (comma-separated: claude,codex,qwen,grok,jcode)
  -h, --help            help for audit
      --json            Print the report as JSON
      --show            Also print the lines that match a retired term
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

* [dot ai memory](dot_ai_memory.md)	 - Audit and archive native agent memory

