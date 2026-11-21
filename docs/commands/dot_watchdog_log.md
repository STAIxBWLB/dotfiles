## dot watchdog log

Tail the watchdog JSON-lines event log

```
dot watchdog log [<count>] [flags]
```

### Examples

```
  dot watchdog log
  dot watchdog log 50
```

### Options

```
  -h, --help[=true|false]   help for log
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

* [dot watchdog](dot_watchdog.md)	 - Reap runaway processes and alert on host health

