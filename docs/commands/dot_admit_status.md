## dot admit status

Show admission owners, pressure and effective limits

### Synopsis

Report the resource-admission controller's view: active slot owners
(scope, class, owner, pid, cwd, since, one line per held slot index), the
effective value of every admission limit with the layer each came from
(default, config or relax), the rules a one-off bypass or an active relax
override steps past, an active relax override with its expiry, the
host-pressure evidence with per-probe availability, the last WindowServer
watchdog evidence, and the hysteresis countdown when a defer episode is
recovering. The evaluation is read-only: status never advances or resets the
recovery window and never removes an expired override. Jobs launched outside
admission wrappers hold no lease and are caught only by the admit-time process
scan.

```
dot admit status [flags]
```

### Examples

```
  dot admit status
  dot admit status --json
```

### Options

```
  -h, --help[=true|false]   help for status
      --json[=true|false]   print the diagnostic bundle as JSON
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

* [dot admit](dot_admit.md)	 - Run one heavy job per repo behind the host-pressure gate

