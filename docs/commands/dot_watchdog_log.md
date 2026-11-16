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
  -h, --help   help for log
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

* [dot watchdog](dot_watchdog.md)	 - Reap runaway processes and alert on host health

