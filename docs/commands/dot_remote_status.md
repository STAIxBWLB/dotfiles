## dot remote status

Show host load, runs, mirrors and image age

### Synopsis

Report the host's own load against its limit, the runs this machine holds
there (mirror path, container, owner, age), the size of every mirror and bare
repository, and how old the dev image is. A host that cannot answer still shows
the local half: the leases this machine holds are facts the host does not have
to confirm.

```
dot remote status [flags]
```

### Examples

```
  dot remote status
  dot remote status --json
```

### Options

```
  -h, --help   help for status
      --json   print the status as JSON
```

### Options inherited from parent commands

```
      --ascii           use ASCII markers regardless of locale
      --color string    color output: auto, always or never (default "auto")
      --config string   path to custom config YAML
      --dry-run         show what would be done without executing
      --home string     override home directory (for admin setup of other users)
      --host string     remote host to use (default: the matching project's host, then remote.default)
      --verbose         show library and debug logs on stderr
      --wait duration   retry a held lease for this long before deferring (default 0: defer at once)
      --yes             unattended mode (skip all prompts)
```

### SEE ALSO

* [dot remote](dot_remote.md)	 - Run builds and tests in a container on an ssh-reachable host

