## dot admit

Run one heavy job per repo behind the host-pressure gate

### Synopsis

Admit a heavy job (build, full test run, indexing, bulk copy, dependency
update) through the resource-admission controller. The gate defers while the
host shows memory pressure warning/critical, verifiable thermal pressure, a
WindowServer watchdog termination inside its grace window, or CPU/load
saturation sustained past the policy thresholds; recovery requires five
continuous minutes of normal telemetry, and admit keeps sampling through
that countdown within --wait. Thresholds come from policy defaults, then the
admission block of ~/.config/dotfiles/config.yaml, then an active
'dot admit relax' override. One heavy slot is held per project repo (shared
across its worktrees, branches, and sessions); different repos run in
parallel, and admission.slots can raise that count. --class maintenance takes
the host-wide maintenance slot instead. On defer the exit code is 75
(EX_TEMPFAIL) with a machine-readable outcome on stdout when --json is set.
When a command runs, its own stdout is the payload and the --json completion
record goes to stderr, so the exit code is the machine-readable result.
Selected-tooling updates share these slots. 'dot peer sync' and 'dot sync
push' hold a background lease while they run, so their rsync never defers
other work. Heavy work launched outside all of these holds no lease; a
bounded process-table scan defers when such work runs in this repo or its
repo is unknown. Use '--' before commands that collide with
subcommand names.

--ignore pressure,slot,uncovered,remote-load (or
DOT_ADMISSION_IGNORE=pressure,slot,uncovered,remote-load, or 'all') steps past
those rules for this run only. Nothing is skipped silently: a bypassed
pressure gate still samples and records history and names what it bypassed on
stderr, a bypassed slot still takes a lease in an overflow slot past the
configured capacity, a bypassed scan still runs and prints what it found, and
a bypassed remote-load probe still reads the host and names the load it found.
Slot scans and leases stay honest; --json adds "ignored".

The child receives CARGO_BUILD_JOBS=2, RUST_TEST_THREADS=2 and GOMAXPROCS=2,
replacing inherited values unless --no-limit-threads is set. Tool-specific
flags (Go -p 2 -parallel 2; browser workers=1) remain the caller's choice.
--project selects both the repository slot and the child's working directory.
--dry-run previews the command without probes or state writes.

```
dot admit [--class heavy|maintenance] [--wait 30m] [--json] [--ignore pressure,slot,uncovered,remote-load|all] -- <command> [args...] [flags]
```

### Options

```
      --class string       slot class: heavy (per-repo) or maintenance (host-wide) (default "heavy")
  -h, --help               help for admit
      --ignore string      rules to step past for this run: pressure, slot, uncovered, remote-load, or all
      --json               print the outcome as JSON
      --no-limit-threads   Keep inherited worker settings instead of limiting Cargo, Rust tests and Go to 2
      --project string     Checkout whose slot and working directory to use (defaults to current directory)
      --wait duration      maximum time to wait for the slot before deferring (default 30m0s)
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
* [dot admit relax](dot_admit_relax.md)	 - Set or clear a temporary override of the admission limits
* [dot admit status](dot_admit_status.md)	 - Show admission owners, pressure evidence, effective limits and hysteresis state

