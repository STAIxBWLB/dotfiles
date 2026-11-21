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
  -h, --help[=true|false]      help for apply
      --json[=true|false]      emit a versioned JSON report
      --persist[=true|false]   save the supplied policy into dotfiles user state after applying overlays
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

* [dot ai policy](dot_ai_policy.md)	 - Inspect, resolve and reconcile AI operating policies

