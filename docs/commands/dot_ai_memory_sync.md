## dot ai memory sync

Replicate the claude-mem store with a peer over ssh

### Synopsis

Replicate observations, session summaries, and their sdk_sessions rows
with a peer Mac. Incremental by default (receiver's MAX(created_at) minus a
7-day overlap); --full re-offers everything and dedupe absorbs it. The peer
needs only the dot binary: the transport is
'ssh <target> dot ai memory sync --serve <op>' with JSON over stdio.

A run is bounded by --timeout: past it, or on SIGTERM, the open import
rolls back and the run exits non-zero naming the phase it stopped in.
Each phase logs its start and end to stderr.

sessions replicates all sessions both ways. status prints both sides'
counts plus the last scheduled run. With no action, sync runs both
directions. --peer defaults to the configured dot peer target.

```
dot ai memory sync [push|pull|sync|sessions|status] [flags]
```

### Options

```
      --full               disable the incremental cutoff (re-offer all rows)
  -h, --help               help for sync
      --peer string        ssh target to sync with (default: the dot peer target)
      --remote-db string   DB path on the peer (default: the peer's ~/.claude-mem/claude-mem.db)
      --timeout duration   abort the run (and roll back an open import) after this long (default 10m0s)
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

* [dot ai memory](dot_ai_memory.md)	 - Manage shared claude-mem integration for Codex, Kimi, Kiro, Copilot, Qwen, and pi

