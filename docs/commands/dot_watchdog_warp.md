## dot watchdog warp

Run one WARP self-heal pass (the root daemon invokes this)

### Synopsis

Probe WARP health (warp-cli Connected AND an interface address in
100.96.0.0/12), fold it into the consecutive-failure state, and act: after
fail_threshold consecutive failures, warp-cli disconnect/connect, up to 3
attempts; then launchctl kickstart -k the WARP daemon, rate-limited to one
restart per 30 minutes. Every action is logged and notified.

```
dot watchdog warp [flags]
```

### Examples

```
  dot watchdog warp
```

### Options

```
  -h, --help[=true|false]   help for warp
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

