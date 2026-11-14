## dot remote

Run builds and tests in a container on an ssh-reachable host

### Synopsis

Mirror a local git checkout to a host you can ssh to and run a command
there in a container. The local machine stays the only place source is edited:
a run sends git's view of the tree (tracked files plus untracked ones no ignore
rule covers), build outputs and secrets never leave the laptop, and nothing is
ever copied back. Results come back as the command's output and exit code.

Nothing is enabled until ~/.config/dotfiles/config.yaml carries a 'remote:'
block (see docs/guides/servers.md). A run holds two admission leases: one per
mirror path, so a repository and each of its worktrees run at most one job at a
time, and one per host, at most max_runs of them. Both show in
'dot admit status'. A run defers with exit 75 (EX_TEMPFAIL) when a lease is
held or the host's own load is at or over its limit; --wait retries a held
lease first, and DOT_ADMISSION_IGNORE=remote-load (or a
'dot admit relax --ignore remote-load' override) steps past the load check for
one run without relaxing anything local.

Interrupting a run removes its container. After a kill the next run of the
same path removes the one left behind.

'dot remote verify' runs a repo's own gate on the exact commit (or, with
--dirty, the working tree as it is) in a fresh checkout on the host, and
'dot remote src' clones and analyzes third-party code only on the host.

### Options

```
  -h, --help            help for remote
      --host string     remote host to use (default: the matching project's host, then remote.default)
      --wait duration   retry a held lease for this long before deferring (default 0: defer at once)
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
* [dot remote job](dot_remote_job.md)	 - Run file-in, file-out jobs on a host's job profile, or locally when it is busy
* [dot remote prune](dot_remote_prune.md)	 - List, and with --yes remove, what runs left behind on the host
* [dot remote run](dot_remote_run.md)	 - Mirror a checkout to the host and run a command in a container there
* [dot remote setup](dot_remote_setup.md)	 - Check the host and build or pull the dev image
* [dot remote src](dot_remote_src.md)	 - Clone or analyze third-party source on the host, never on this machine
* [dot remote status](dot_remote_status.md)	 - Show the host's load, this machine's runs there, mirror sizes and image age
* [dot remote verify](dot_remote_verify.md)	 - Run a repo's gate on its exact commit in a fresh checkout on the host

