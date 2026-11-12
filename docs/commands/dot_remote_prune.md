## dot remote prune

List, and with --yes remove, what runs left behind on the host

### Synopsis

List what runs left behind on the host: mirrors whose local checkout
(remote.local_root/<path>) no longer exists, verify refs a killed verify did
not delete, and containers carrying the host's prefix that are not running.
Nothing is deleted without --yes. A mirror a run holds (its claim on the host,
or a lease on this machine) is skipped, and a removal takes the mirror's claim
itself so a run starting meanwhile defers instead of syncing into it.

Job directories 'dot remote job' kept after a failure are listed too, but
never removed: their out/ may be the only copy of what the job made.

```
dot remote prune [--yes] [flags]
```

### Options

```
  -h, --help   help for prune
      --json   print the report as JSON
```

### Options inherited from parent commands

```
      --config string   Path to custom config YAML
      --dry-run         Show what would be done without executing
      --home string     Override home directory (for admin setup of other users)
      --host string     remote host to use (default: the matching project's host, then remote.default)
      --wait duration   retry a held lease for this long before deferring (default 0: defer at once)
      --yes             Unattended mode (skip all prompts)
```

### SEE ALSO

* [dot remote](dot_remote.md)	 - Run builds and tests in a container on an ssh-reachable host

