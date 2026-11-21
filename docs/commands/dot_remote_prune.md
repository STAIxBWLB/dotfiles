## dot remote prune

List or remove what runs left behind on the host

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

### Examples

```
  dot remote prune
  dot remote prune --yes
```

### Options

```
  -h, --help[=true|false]   help for prune
      --json[=true|false]   print the report as JSON
```

### Options inherited from parent commands

```
      --ascii[=true|false]     use ASCII markers regardless of locale
      --color string           color output: auto, always or never (default "auto")
      --config string          path to custom config YAML
      --dry-run[=true|false]   show what would be done without executing
      --home string            override home directory (for admin setup of other users)
      --host string            remote host to use (default: the matching project's host, then remote.default)
      --verbose[=true|false]   show library and debug logs on stderr
      --wait duration          retry a held lease for this long before deferring (default 0: defer at once)
      --yes[=true|false]       unattended mode (skip all prompts)
```

### SEE ALSO

* [dot remote](dot_remote.md)	 - Run builds and tests in a container on an ssh-reachable host

