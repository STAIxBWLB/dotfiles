## dot remote hold

Reserve a remote host slot while running a local command

### Synopsis

Reserve one of the host's max_runs slots and check the host's load,
then run COMMAND locally with its input and output attached. The slot is
released when COMMAND exits. The host defaults to remote.default.

A held slot or a host at its load limit defers with exit 75. With --dry-run,
show the slot and the host load without reserving capacity.

```
dot remote hold -- <command> [<arg>...] [flags]
```

### Examples

```
  dot remote hold -- sleep 60
  dot remote hold --dry-run --json -- sleep 60
```

### Options

```
  -h, --help[=true|false]   help for hold
      --json[=true|false]   print the defer or dry-run record as JSON
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

