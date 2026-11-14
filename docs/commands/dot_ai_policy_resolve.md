## dot ai policy resolve

Select a validated configuration for one task without launching it

```
dot ai policy resolve [flags]
```

### Options

```
      --agent string      Explicit agent selection (never silently substituted)
  -h, --help              help for resolve
      --json              Emit the versioned app integration contract
      --origin string     Calling surface, such as cli, maru or orca (default "cli")
      --project string    Task working directory (defaults to current directory)
      --require strings   Required validated capabilities
      --task string       Task intent used for automatic workload classification
      --unattended        Require a supported unattended approval path
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

* [dot ai policy](dot_ai_policy.md)	 - Inspect, resolve and reconcile adaptive AI operating policies

