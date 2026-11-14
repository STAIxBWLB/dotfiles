## dot watchdog notify

Send a watchdog alert (monit invokes this; run it by hand to test alerts)

```
dot watchdog notify <level> <message> [flags]
```

### Options

```
  -h, --help             help for notify
      --profile string   Profile name (minimal, full, server)
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

* [dot watchdog](dot_watchdog.md)	 - Reap runaway processes and alert on host health

