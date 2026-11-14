## dot admit relax

Set or clear a temporary override of the admission limits

### Synopsis

Override admission for a bounded time. --ignore steps past the named rules
until the override expires; --set layers the same keys the admission block of
~/.config/dotfiles/config.yaml takes, relative to admission.
Every evaluation still measures what it bypassed, so the limits come back
correctly when the override ends: a bypassed pressure gate still samples and
records history, a bypassed slot still takes a lease (in an overflow slot past
the configured capacity), a bypassed scan still runs and names what it found,
and a bypassed remote-load probe still reads the remote host. An expired
override is ignored and removed by the next evaluation.

--set keys and examples:
  thresholds.recover_sustain=1m      thresholds.idle_defer_below=5
  prebuilt.idle_defer_below=10       prebuilt.safety_window=10s
  slots.heavy_per_repo=2             slots.maintenance=2
  uncovered.enabled=false            uncovered.cpu_percent=0
  uncovered.ignore_names=node,rustc

The override is stored in ~/.local/state/dot/admission/override.json.
--dry-run prints the override that would be written (or cleared) and writes
nothing.

```
dot admit relax --for 1h [--ignore pressure,slot,uncovered,remote-load|all] [--set <key>=<value> ...] | relax --clear [flags]
```

### Options

```
      --clear             Remove the temporary override now
      --for duration      how long the override lasts (maximum 24h) (default 1h0m0s)
  -h, --help              help for relax
      --ignore string     rules to step past until the override expires: pressure, slot, uncovered, remote-load, or all
      --set stringArray   policy key to layer until the override expires, as <key>=<value> (repeatable)
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

* [dot admit](dot_admit.md)	 - Run one heavy job per repo behind the host-pressure gate

