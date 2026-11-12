## dot remote job

Run file-in, file-out jobs on a host's job profile, or locally when it is busy

### Synopsis

A job sends named input files to the host, runs a command in an isolated job
directory there, and brings the files it wrote back. When the host cannot take
the job, the same command runs on this machine under 'dot admit'.

Profiles live under remote.hosts.<host>.jobs (see docs/guides/servers.md), each
with its own image: build or pull it with 'dot remote setup --profile P'.

### Options

```
  -h, --help             help for job
      --profile string   job profile (default: the host's only one)
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
* [dot remote job check](dot_remote_job_check.md)	 - Exit 0 when the host can take a job now, 69 when not
* [dot remote job run](dot_remote_job_run.md)	 - Stage files, run a command in a job directory, collect out/

