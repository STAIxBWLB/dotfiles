## dot container setup

Install the backend and write the container shim

### Synopsis

Set up the container command on this host: install the backend and write
the shim and its state file, then enable the container module in user state
and render the shim's PATH entry (the shell module). Afterwards it is
equivalent to dot apply --module container. A host the module cannot set up
keeps its config and shell files untouched.

The opt-in in user state syncs to your other machines, but each host chooses
for itself: running setup is this host's yes, and it overrides an earlier no.

On Linux an existing, usable docker is used as is. With no docker or podman,
setup offers to install the distro docker package (--yes accepts) and falls
back to podman when declined or when the docker install fails.

```
dot container setup [flags]
```

### Options

```
  -h, --help             help for setup
      --profile string   Profile name (minimal, full, server)
```

### Options inherited from parent commands

```
      --color string    Color output: auto, always or never (default "auto")
      --config string   Path to custom config YAML
      --dry-run         Show what would be done without executing
      --home string     Override home directory (for admin setup of other users)
      --verbose         Show library and debug logs on stderr
      --yes             Unattended mode (skip all prompts)
```

### SEE ALSO

* [dot container](dot_container.md)	 - Shared container command (apple/container on macOS, docker or podman on Linux)

