## dot watchdog reap

Run one reaper pass (the LaunchAgent invokes this)

### Synopsis

Sample the process table once, fold it into the cross-run history, and
act on every process that matches the watchdog config, is not allowlisted,
and has held CPU at or above the threshold for at least the sustain window.
dry-run mode only logs and notifies; enforce mode sends SIGTERM, waits the
kill grace, then SIGKILLs.

```
dot watchdog reap [flags]
```

### Options

```
  -h, --help   help for reap
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

