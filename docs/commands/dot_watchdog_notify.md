## dot watchdog notify

Send a watchdog alert (monit runs this)

```
dot watchdog notify <level> <message> [flags]
```

### Examples

```
  dot watchdog notify warn "disk almost full"
```

### Options

```
  -h, --help             help for notify
      --profile string   profile name (minimal, full, server)
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

