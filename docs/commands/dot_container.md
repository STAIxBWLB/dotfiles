## dot container

Shared container command (apple/container on macOS, docker or podman on Linux)

### Synopsis

One `container` command with Apple container syntax on every host.

On macOS it runs apple/container (Apple silicon, macOS 26+). On Linux a shim
translates the same syntax for docker, or podman when no docker is usable.
The module is opt-in: dot container setup enables it in user state. That
opt-in syncs with your config, but each host chooses whether to install: dot
apply asks a host that has not chosen (from a terminal only; --yes and
scheduled runs never install there), and dot container setup is a yes.

Config (~/.config/dotfiles/config.yaml):
  modules:
    container:
      backend: auto   # auto | apple | docker | podman
      dns: []         # default --dns servers, e.g. [1.1.1.1, 1.0.0.1]

### Options

```
  -h, --help   help for container
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

* [dot](dot.md)	 - User environment & workspace management tool
* [dot container setup](dot_container_setup.md)	 - Install the backend and write the container shim
* [dot container status](dot_container_status.md)	 - Show the container backend, shim, DNS defaults and legacy snippet

