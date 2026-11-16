## dot ai policy resolve

Select a validated configuration for one task

```
dot ai policy resolve [flags]
```

### Examples

```
  dot ai policy resolve --task 'fix the flaky test'
  dot ai policy resolve --agent codex --json
```

### Options

```
      --agent string      explicit agent selection (never silently substituted)
  -h, --help              help for resolve
      --json              emit the versioned app integration contract
      --origin string     calling surface, such as cli, maru or orca (default "cli")
      --project string    task working directory (defaults to current directory)
      --require strings   required validated capabilities
      --task string       task intent used for automatic workload classification
      --unattended        require a supported unattended approval path
      --workload string   auto, routine, implementation, deep-analysis, independent-review, documents-teaching, visual-production (default "auto")
```

### Options inherited from parent commands

```
      --ascii           Use ASCII markers regardless of locale
      --color string    Color output: auto, always or never (default "auto")
      --config string   Path to custom config YAML
      --dry-run         Show what would be done without executing
      --home string     Override home directory (for admin setup of other users)
      --verbose         Show library and debug logs on stderr
      --yes             Unattended mode (skip all prompts)
```

### SEE ALSO

* [dot ai policy](dot_ai_policy.md)	 - Inspect, resolve and reconcile AI operating policies

