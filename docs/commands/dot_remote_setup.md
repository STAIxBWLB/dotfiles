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

### Examples

```
  dot remote setup
  dot remote setup --profile media
```

### Options

```
  -h, --help[=true|false]   help for setup
      --json[=true|false]   print the report as JSON
      --profile string      build or pull a job profile's image instead of the dev image (remote.hosts.<host>.jobs.<profile>)
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

