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

### Options

```
  -h, --help   help for watchdog
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

* [dot](dot.md)	 - User environment & workspace management tool
* [dot watchdog log](dot_watchdog_log.md)	 - Tail the watchdog JSON-lines event log
* [dot watchdog notify](dot_watchdog_notify.md)	 - Send a watchdog alert (monit invokes this; run it by hand to test alerts)
* [dot watchdog reap](dot_watchdog_reap.md)	 - Run one reaper pass (the LaunchAgent invokes this)
* [dot watchdog setup](dot_watchdog_setup.md)	 - Install the watchdog reaper agent, WARP heal daemon, monit supervision, Beszel agent, and optional power hardening (macOS)
* [dot watchdog status](dot_watchdog_status.md)	 - Show watchdog config, LaunchAgent, and sample state
* [dot watchdog uninstall](dot_watchdog_uninstall.md)	 - Remove the watchdog reaper agent, WARP heal daemon, monit supervision, and Beszel agent plist (macOS)
* [dot watchdog warp](dot_watchdog_warp.md)	 - Run one WARP self-heal pass (the root daemon invokes this)

