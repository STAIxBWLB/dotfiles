## dot ai memory archive

Move stale memory files into an archive (dry run unless --apply)

### Synopsis

Move whole memory files out of the layer an agent loads. Content is never
edited or deleted, and index files are never touched.

  claude, qwen  <memory dir>/_archive/<path>: outside the index, and outside
                the */memory/*.md files amem imports
  codex         $CODEX_HOME/memories-archive/<path>: outside the directory
                Codex consolidates. memory_summary.md, MEMORY.md,
                raw_memories.md and rollout_summaries/ are refused, because
                the next consolidation rewrites them; move their source notes
                (for example extensions/ad_hoc/notes/*) instead.

Without --apply, each move is previewed with the index lines that still name
the file. With --apply the files are moved, one 'dot ai audit' event is
written, and the command that moves them back is printed. Remove the listed
index lines yourself or ask the agent to.

Refused: directories, paths outside the agent's memory store, symlinks that
resolve outside it, the store's own index, and a destination that exists.

```
dot ai memory archive <agent> <path>... [flags]
```

### Options

```
      --apply   Move the files (default: preview)
  -h, --help    help for archive
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

* [dot ai memory](dot_ai_memory.md)	 - Audit and archive native agent memory, or retire claude-mem

