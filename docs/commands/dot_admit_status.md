## dot admit status

Show admission owners, pressure evidence, effective thresholds and hysteresis state

### Synopsis

Report the resource-admission controller's view: active slot owners
(scope, class, owner, pid, cwd, since), the effective pressure thresholds with
the layer each value came from (default, config or relax), an active relax
override with its expiry, the host-pressure evidence with per-probe
availability, the last WindowServer watchdog evidence, and the hysteresis
countdown when a defer episode is recovering. The evaluation is
read-only: status never advances or resets the recovery window and never
removes an expired override. Jobs launched outside admission wrappers hold no
lease and are caught only by the admit-time process scan.

```
dot admit status [flags]
```

### Options

```
  -h, --help   help for status
      --json   print the diagnostic bundle as JSON
```

### Options inherited from parent commands

```
      --config string   Path to custom config YAML
      --dry-run         Show what would be done without executing
      --home string     Override home directory (for admin setup of other users)
      --yes             Unattended mode (skip all prompts)
```

### SEE ALSO

* [dot admit](dot_admit.md)	 - Run one heavy job per repo behind the host-pressure gate

