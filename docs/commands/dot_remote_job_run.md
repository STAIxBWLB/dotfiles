## dot remote job run

Stage files, run a command in a job directory, collect out/

### Synopsis

Stage each -i FILE in the job's in/, run COMMAND with the job directory as its
working directory, and collect out/ into -o DIR. Write COMMAND with relative
paths (in/..., out/...) so it runs unchanged on the host or locally. One
COMMAND argument is a shell script; several are an argv.

The host is checked first (image present, load under max_load_ratio, free
space above min_free_gb). When it cannot take the job and the profile's
fallback is local, the job runs here in <local.root>/<name> under 'dot admit'
with in/ holding symlinks to the inputs. A job that fails, or whose out/
cannot be collected, keeps its directory and prints where.

Exit status: the command's own; 75 when the profile's max_jobs are already
active from this machine; 69 when the host cannot take the job and
--remote-only (or fallback: none) keeps it off this machine; 128+signal when
interrupted.

```
dot remote job run -o DIR [-i FILE]... [--name N] [--remote-only | --local-only] -- COMMAND [ARG...] [flags]
```

### Options

```
  -h, --help                help for run
  -i, --input stringArray   input file staged in in/ (repeatable; base names must be unique)
      --json                print the defer or unavailable record as JSON
      --local-only          skip the host and run on this machine under dot admit
      --name string         job name, [a-z0-9][a-z0-9-]{0,63} (default: a timestamp)
  -o, --out string          directory out/ is collected into (created if missing)
      --remote-only         exit 69 instead of running locally when the host cannot take the job
```

### Options inherited from parent commands

```
      --ascii            Use ASCII markers regardless of locale
      --color string     Color output: auto, always or never (default "auto")
      --config string    Path to custom config YAML
      --dry-run          Show what would be done without executing
      --home string      Override home directory (for admin setup of other users)
      --host string      remote host to use (default: the matching project's host, then remote.default)
      --profile string   job profile (default: the host's only one)
      --verbose          Show library and debug logs on stderr
      --wait duration    retry a held lease for this long before deferring (default 0: defer at once)
      --yes              Unattended mode (skip all prompts)
```

### SEE ALSO

* [dot remote job](dot_remote_job.md)	 - Run file-in, file-out jobs on a host's job profile, or locally when it is busy

