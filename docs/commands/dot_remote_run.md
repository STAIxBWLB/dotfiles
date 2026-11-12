## dot remote run

Mirror a checkout to the host and run a command in a container there

### Synopsis

Mirror the checkout at PATH (default: the current directory) and run CMD in
a container with the mirror at /work/<PATH relative to remote.local_root>. One
CMD argument is a shell script; several are an argv. Caches named in the host's
'caches' block are mounted, so dependency downloads survive between runs.

Everything the command writes stays on the host: the mirror keeps build
outputs between runs and nothing comes back. PATH must be below
remote.local_root, and may not be that root or $HOME.

-p PORT publishes the container's PORT on the host's 127.0.0.1 only and
forwards local 127.0.0.1:PORT to it over the run's own ssh connection, for the
life of the run (repeatable). A port already taken here fails the run.

--cgroup gives the command a writable cgroup v2 subtree, its path in
DOT_REMOTE_CGROUP: the container starts as root with CAP_SYS_ADMIN and a private
cgroup namespace only to set the subtree up, then drops to the image's own user
with every capability removed before the command runs. The host must use
cgroup v2 and let the runtime grant CAP_SYS_ADMIN with AppArmor unconfined.

```
dot remote run [PATH] -- COMMAND [ARG...] [flags]
```

### Options

```
      --cgroup      delegate a writable cgroup v2 subtree to the command (path in DOT_REMOTE_CGROUP)
  -h, --help        help for run
      --json        print the defer record as JSON
  -p, --port ints   publish PORT on the host loopback and forward it to local 127.0.0.1:PORT (repeatable)
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

