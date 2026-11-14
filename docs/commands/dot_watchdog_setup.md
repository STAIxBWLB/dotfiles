## dot watchdog setup

Install the watchdog reaper agent, WARP heal daemon, monit supervision, Beszel agent, and optional power hardening (macOS)

### Synopsis

Install the watchdog on this Mac: the user-domain reaper LaunchAgent,
plus the root WARP heal LaunchDaemon when watchdog.warp is enabled, the
monit supervision agent when watchdog.monit is enabled, and the Beszel
external-monitoring agent when watchdog.beszel is enabled (a missing
hub_url or secrets-managed env file skips that step with a warning).
--headless additionally applies power hardening (pmset sleep 0 on charger,
autorestart, womp, restartfreeze on) after saving the prior values for
uninstall-time restore; it requires watchdog.power.headless: true.

```
dot watchdog setup [flags]
```

### Options

```
      --headless         apply headless power hardening (requires watchdog.power.headless: true)
  -h, --help             help for setup
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

