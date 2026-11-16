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

### Examples

```
  dot remote job check
  dot remote job check --json
```

### Options

```
  -h, --help   help for check
      --json   print the result as JSON
```

### Options inherited from parent commands

```
      --ascii            use ASCII markers regardless of locale
      --color string     color output: auto, always or never (default "auto")
      --config string    path to custom config YAML
      --dry-run          show what would be done without executing
      --home string      override home directory (for admin setup of other users)
      --host string      remote host to use (default: the matching project's host, then remote.default)
      --profile string   job profile (default: the host's only one)
      --verbose          show library and debug logs on stderr
      --wait duration    retry a held lease for this long before deferring (default 0: defer at once)
      --yes              unattended mode (skip all prompts)
```

### SEE ALSO

* [dot remote job](dot_remote_job.md)	 - Run file-in, file-out jobs remotely or locally

