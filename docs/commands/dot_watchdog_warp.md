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

### Options

```
  -h, --help   help for warp
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

