## dot watchdog notify

Send a watchdog alert (monit invokes this; run it by hand to test alerts)

```
dot watchdog notify <level> <message> [flags]
```

### Options

```
  -h, --help   help for notify
```

### Options inherited from parent commands

```
      --config string    Path to custom config YAML
      --dry-run          Show what would be done without executing
      --home string      Override home directory (for admin setup of other users)
      --profile string   Profile name (minimal, full, server)
      --yes              Unattended mode (skip all prompts)
```

### SEE ALSO

* [dot watchdog](dot_watchdog.md)	 - Reap runaway processes and alert on host health

