## dot ai session start

Launch an eligible agent or supervise checkpointed JSONL turns

### Synopsis

Start a native agent with the resolved model, effort and automatic-review flags.
Use --task for an interactive session, or --input FILE (or -) for a trusted
supervisor's JSONL turns. Managed turns re-evaluate workload at safe boundaries;
continuations require a curated checkpoint. Same-provider turns use native resume;
Claude/Codex handoffs start a new session from scope and completed-effect evidence.
Manual native/Orca sessions are not taken over. Heavy commands still use dot admit.

```
dot ai session start [flags]
```

### Options

```
      --agent string      Explicit agent selection (never silently substituted)
      --fixed             Freeze the initial configuration for this session
  -h, --help              help for start
      --input string      Trusted JSONL turn stream file, or - for stdin
      --origin string     Calling surface, such as cli, maru or orca (default "cli")
      --project string    Task working directory (defaults to current directory)
      --require strings   Required validated capabilities
      --task string       Task intent used for automatic workload classification
      --unattended        Require a supported unattended approval path
      --workload string   auto, routine, implementation, deep-analysis, independent-review, documents-teaching, visual-production (default "auto")
```

### Options inherited from parent commands

```
      --config string   Path to custom config YAML
      --dry-run         Show what would be done without executing
      --home string     Override home directory (for admin setup of other users)
      --yes             Unattended mode (skip all prompts)
```

### SEE ALSO

* [dot ai session](dot_ai_session.md)	 - Launch controlled agents with adaptive policy and native continuation

