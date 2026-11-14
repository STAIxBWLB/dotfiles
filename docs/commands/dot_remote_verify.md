## dot remote verify

Run a repo's gate on its exact commit in a fresh checkout on the host

### Synopsis

Push PATH's exact commit (HEAD, or with --dirty a throwaway commit of the
working tree) to an owner-only bare repository on the host, check it out fresh
in a container next to the base branch, and run COMMAND there. Without a
command, the matching remote.projects entry's 'verify' runs. The real git
history lets a repo's own gate (diff against main, commit checks) run unchanged.

--dirty stages the working tree (tracked files plus untracked ones no ignore
rule covers) in a throwaway index and writes a commit object on top of HEAD:
the checkout's index, branches and refs are untouched. The base is
remote.projects.<path>.base, or the remote's default branch. The pushed ref is
deleted when the job ends. verify and run of one checkout exclude each other.

```
dot remote verify [PATH] [--dirty] [-- COMMAND [ARG...]] [flags]
```

### Options

```
      --dirty   verify the working tree as it is, not HEAD (index and refs untouched)
  -h, --help    help for verify
      --json    print the defer record as JSON
```

### Options inherited from parent commands

```
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

