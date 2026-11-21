## dot watchdog setup

Install the watchdog agents and supervision (macOS)

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

### Examples

```
  dot watchdog setup
```

### Options

```
      --headless[=true|false]   apply headless power hardening (requires watchdog.power.headless: true)
  -h, --help[=true|false]       help for setup
      --profile string          profile name (minimal, full, server)
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

