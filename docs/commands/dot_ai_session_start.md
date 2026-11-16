## dot ai session start

Launch an agent or supervise checkpointed JSONL turns

### Synopsis

Start a native agent with the resolved model, effort and automatic-review flags.
Use --task for an interactive session, or --input FILE (or -) for a trusted
supervisor's JSONL turns. Managed turns re-evaluate workload at safe
boundaries; continuations require a curated checkpoint. Same-provider turns
use native resume; Claude/Codex handoffs start a new session from scope and
completed-effect evidence. Manual native/Orca sessions are not taken over.
Heavy commands still use dot admit.

```
dot ai session start [flags]
```

### Examples

```
  dot ai session start --task 'review the open PR'
  dot ai session start --input turns.jsonl
```

### Options

```
      --agent string      explicit agent selection (never silently substituted)
      --fixed             freeze the initial configuration for this session
  -h, --help              help for start
      --input string      trusted JSONL turn stream file, or - for stdin
      --origin string     calling surface, such as cli, maru or orca (default "cli")
      --project string    task working directory (defaults to current directory)
      --require strings   required validated capabilities
      --task string       task intent used for automatic workload classification
      --unattended        require a supported unattended approval path
      --workload string   auto, routine, implementation, deep-analysis, independent-review, documents-teaching, visual-production (default "auto")
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

* [dot ai session](dot_ai_session.md)	 - Launch controlled agents with adaptive policy

