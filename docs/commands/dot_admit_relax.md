## dot admit relax

Set or clear a temporary override of the host-pressure gate

### Synopsis

Override the host-pressure gate for a bounded time. With
--ignore-pressure the gate admits regardless of telemetry until the override
expires; with threshold flags the same keys as admission.thresholds in
~/.config/dotfiles/config.yaml are layered over the config until then. Every
evaluation still samples and records pressure history, so recovery stays
continuous after the override ends. An expired override is ignored and
removed by the next gate evaluation. Slot exclusivity and the uncovered-work
scan are never relaxed.

Threshold flags: --idle-defer-below, --idle-recover-at (percent),
--load-defer-frac, --load-recover-frac (fraction of the CPU count),
--defer-sustain, --recover-sustain, --ws-grace (Go durations such as 1m),
--mem-avail-warn-pct, --mem-avail-critical-pct (Linux, percent).

The override is stored in ~/.local/state/dot/admission/override.json.

```
dot admit relax [--for 1h] [--ignore-pressure] [--<threshold> value ...] [--clear] [flags]
```

### Options

```
      --clear                          Remove the temporary override now
      --defer-sustain duration         how long a CPU/load excursion must last to defer
      --for duration                   how long the override lasts (maximum 24h) (default 1h0m0s)
  -h, --help                           help for relax
      --idle-defer-below float         CPU idle percent below which work defers
      --idle-recover-at float          CPU idle percent required to recover
      --ignore-pressure                Admit regardless of host pressure until the override expires
      --load-defer-frac float          load1 at this multiple of the CPU count defers
      --load-recover-frac float        load1 below this multiple of the CPU count recovers
      --mem-avail-critical-pct float   Linux MemAvailable percent that is critical
      --mem-avail-warn-pct float       Linux MemAvailable percent that warns
      --recover-sustain duration       how long normal telemetry must last to recover
      --ws-grace duration              WindowServer watchdog termination grace window
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

