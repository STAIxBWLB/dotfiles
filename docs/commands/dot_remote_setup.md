## dot remote setup

Check the host and build or pull the dev image

### Synopsis

Check that the host answers over ssh in batch mode and has git, rsync 3.2.3
or newer, and a container runtime, then build the image from your Dockerfile
(with the host user's UID/GID as build arguments) or pull the prebuilt ref.
Re-run it after editing the Dockerfile: it is the only thing that rebuilds the
image. dot ships no image of its own; the servers guide carries a starter.

```
dot remote setup [flags]
```

### Options

```
  -h, --help             help for setup
      --json             print the report as JSON
      --profile string   build or pull a job profile's image instead of the dev image (remote.hosts.<host>.jobs.<profile>)
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

