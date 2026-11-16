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

