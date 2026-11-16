## dot watchdog

Reap runaway processes and alert on host health

### Synopsis

Watchdog guards this host against runaway orphaned processes (reaper),
with WARP connectivity heal, monit service-health supervision, and optional
headless power hardening. The reaper runs from a user LaunchAgent installed by
'dot watchdog setup'; it is disabled by default and opted in per host via
the watchdog section of the profile config.

```
dot watchdog [flags]
```

### Examples

```
  dot watchdog --help
```

### Options

```
  -h, --help   help for watchdog
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

* [dot](dot.md)	 - User environment & workspace management tool
* [dot watchdog log](dot_watchdog_log.md)	 - Tail the watchdog JSON-lines event log
* [dot watchdog notify](dot_watchdog_notify.md)	 - Send a watchdog alert (monit runs this)
* [dot watchdog reap](dot_watchdog_reap.md)	 - Run one reaper pass (the LaunchAgent invokes this)
* [dot watchdog setup](dot_watchdog_setup.md)	 - Install the watchdog agents and supervision (macOS)
* [dot watchdog status](dot_watchdog_status.md)	 - Show watchdog config, LaunchAgent, and sample state
* [dot watchdog uninstall](dot_watchdog_uninstall.md)	 - Remove the watchdog agents and supervision (macOS)
* [dot watchdog warp](dot_watchdog_warp.md)	 - Run one WARP self-heal pass (the root daemon invokes this)

