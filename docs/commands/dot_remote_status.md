## dot remote status

Show the host's load, this machine's runs there, mirror sizes and image age

### Synopsis

Report the host's own load against its limit, the runs this machine holds
there (mirror path, container, owner, age), the size of every mirror and bare
repository, and how old the dev image is. A host that cannot answer still shows
the local half: the leases this machine holds are facts the host does not have
to confirm.

```
dot remote status [flags]
```

### Options

```
  -h, --help   help for status
      --json   print the status as JSON
```

### Options inherited from parent commands

```
      --ascii           Use ASCII markers regardless of locale
      --color string    Color output: auto, always or never (default "auto")
      --config string   Path to custom config YAML
      --dry-run         Show what would be done without executing
      --home string     Override home directory (for admin setup of other users)
      --host string     remote host to use (default: the matching project's host, then remote.default)
      --verbose         Show library and debug logs on stderr
      --wait duration   retry a held lease for this long before deferring (default 0: defer at once)
      --yes             Unattended mode (skip all prompts)
```

### SEE ALSO

* [dot remote](dot_remote.md)	 - Run builds and tests in a container on an ssh-reachable host

