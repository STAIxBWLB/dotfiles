## dot remote job check

Exit 0 when the host can take a job now, 69 when not

### Synopsis

One ssh round trip (30 seconds at most, 5 to connect): the profile's image is
present, load1 is under max_load_ratio x CPUs, and more than min_free_gb is
free under the job root. A timeout or an unreadable value counts as
unavailable. Exit 0 when the host can take a job, 69 with the reason when not.

```
dot remote job check [flags]
```

### Options

```
  -h, --help   help for check
      --json   print the result as JSON
```

### Options inherited from parent commands

```
      --config string    Path to custom config YAML
      --dry-run          Show what would be done without executing
      --home string      Override home directory (for admin setup of other users)
      --host string      remote host to use (default: the matching project's host, then remote.default)
      --profile string   job profile (default: the host's only one)
      --wait duration    retry a held lease for this long before deferring (default 0: defer at once)
      --yes              Unattended mode (skip all prompts)
```

### SEE ALSO

* [dot remote job](dot_remote_job.md)	 - Run file-in, file-out jobs on a host's job profile, or locally when it is busy

