## dot ai policy apply

Apply owned policy overlays and optionally persist them

```
dot ai policy apply [flags]
```

### Examples

```
  dot ai policy apply
  dot ai policy apply --persist
```

### Options

```
  -h, --help      help for apply
      --json      emit a versioned JSON report
      --persist   save the supplied policy into dotfiles user state after applying overlays
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

* [dot ai policy](dot_ai_policy.md)	 - Inspect, resolve and reconcile AI operating policies

