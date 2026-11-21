## dot ai coauthor-guard apply

Apply coauthor guard instruction and Git commit-msg hook

```
dot ai coauthor-guard apply [flags]
```

### Examples

```
  dot ai coauthor-guard apply --mode warn
  dot ai coauthor-guard apply --mode block --persist
```

### Options

```
      --apply-agents[=true|false]   reapply agents SSOT to live tool targets after updating the instruction
  -h, --help[=true|false]           help for apply
      --mode string                 guard mode: off, warn, or block (default "block")
      --persist[=true|false]        persist modules.git.coauthor_guard for future dot apply runs
```

### Options inherited from parent commands

```
      --ascii[=true|false]     use ASCII markers regardless of locale
      --color string           color output: auto, always or never (default "auto")
      --config string          path to custom config YAML
      --dry-run[=true|false]   show what would be done without executing
      --home string            override home directory (for admin setup of other users)
      --verbose[=true|false]   show library and debug logs on stderr
      --yes[=true|false]       unattended mode (skip all prompts)
```

### SEE ALSO

* [dot ai coauthor-guard](dot_ai_coauthor-guard.md)	 - Warn or block AI-added Co-authored commit trailers

