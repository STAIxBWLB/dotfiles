## dot admit

Run one heavy job per repo behind the host-pressure gate

### Synopsis

Admit a heavy job (build, full test run, indexing, bulk copy, dependency
update) through the resource-admission controller. The gate defers while the
host shows memory pressure warning/critical, verifiable thermal pressure, a
WindowServer watchdog termination inside its grace window, or CPU/load
saturation sustained past the policy thresholds; recovery requires five
continuous minutes of normal telemetry. One heavy slot is held per project
repo (shared across its worktrees, branches, and sessions); different repos
run in parallel. --class maintenance takes the single host-wide maintenance
slot instead. On defer the exit code is 75 (EX_TEMPFAIL) with a
machine-readable outcome on stdout when --json is set. When a command runs,
its own stdout is the payload and the --json completion record goes to
stderr, so the exit code is the machine-readable result. 'dot ai run' and
the tooling updates share these slots. 'dot peer sync' and 'dot sync push'
hold a background lease while they run, so their rsync never defers other
work. Heavy work launched outside all of these holds no lease; a bounded
process-table scan defers when such work runs in this repo or its repo is
unknown. Use '--' before commands that collide with
subcommand names.

```
dot admit [--class heavy|maintenance] [--wait 30m] [--json] -- <command> [args...] [flags]
```

### Options

```
      --class string    slot class: heavy (per-repo) or maintenance (host-wide) (default "heavy")
  -h, --help            help for admit
      --json            print the outcome as JSON
      --wait duration   maximum time to wait for the slot before deferring (default 30m0s)
```

### Options inherited from parent commands

```
      --config string    Path to custom config YAML
      --dry-run          Show what would be done without executing
      --home string      Override home directory (for admin setup of other users)
      --module strings   Run specific modules only
      --profile string   Profile name (minimal, full, server)
      --yes              Unattended mode (skip all prompts)
```

### SEE ALSO

* [dot](dot.md)	 - User environment & workspace management tool
* [dot admit status](dot_admit_status.md)	 - Show admission owners, pressure evidence, and hysteresis state

