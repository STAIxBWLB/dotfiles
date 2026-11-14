# Servers and containers

## Container

The `container` module is opt-in in every profile. `dot container setup`
enables it in user state, installs the backend, and writes the shim
`~/.local/share/dotfiles/shims/container`, which `00-exports.sh` puts ahead of
the brew bin dirs.

The opt-in syncs with `~/.config/dotfiles`, but each host chooses whether to
install: `dot container setup` is that host's yes, and `dot apply` run from a
terminal asks a host that has not chosen (a no is remembered in the host's
`state.json`). `dot apply --yes` and scheduled runs never install on an
undecided host. Every host that is set up takes Apple `container` syntax:

| Host | Backend | Setup installs |
|------|---------|----------------|
| macOS 26+ on Apple silicon | apple/container | `brew install container`, `brew services start container`, the recommended guest kernel |
| Linux with a usable docker | docker | nothing; an existing docker is never changed |
| Linux without docker or podman | docker, or podman when declined | the distro package (`docker.io` or `docker`), `systemctl enable --now docker`, the `docker` group (root-equivalent; log in again) |

Intel Macs and macOS before 26 are reported as unsupported and skipped. On
Linux the shim translates verbs (`list` → `ps`, `image list` → `image ls`,
`registry login` → `login`, `system status` → `info`) and flags (`-c` →
`--cpus`, `-a`/`--os` → `--platform`, `--format yaml` is refused). Verbs with
no Linux meaning (`system start`, `builder start`) are no-ops; `system
kernel`, `machine` and `k8s` exit 2. `DOT_CONTAINER_TRACE=1` prints the argv
the shim runs.

`modules.container.dns` adds `--dns` to `run`, `create`, `build` and
`builder start` (on Linux `run` and `create` only, since docker `build` has
no `--dns`) unless the call already passes `--dns` or `--no-dns`. When
Cloudflare WARP holds port 53, DNS inside apple/container fails, and setup offers `[1.1.1.1, 1.0.0.1]`. `dot container
status [--probe]` reports the backend, service, shim, DNS and WARP state.

## Remote hosts

`dot remote` moves heavy Linux-portable work (builds, full test runs, repo
gates, third-party source analysis) to an ssh-reachable host, without moving
the source: this machine stays the only place you edit, a run sends git's view
of the checkout, and nothing is ever copied back. Results come back as the
command's output and exit code.

The host needs a login you can use non-interactively (key auth: every ssh dot
runs is `BatchMode=yes`), `git`, `rsync` 3.2.3 or newer (openrsync is refused),
and `docker` or `podman`. No toolchain is installed on the host itself: tools
live in the image, so changing them means editing the Dockerfile and running
`dot remote setup` again.

### Configure

```yaml
remote:
  default: buildbox
  hosts:
    buildbox:
      ssh: buildbox                 # ssh destination or alias (default: the host's name)
      cpus: 16                      # container CPU limit, and the default for
      memory: 32g                   # CARGO_BUILD_JOBS and GOMAXPROCS
      max_runs: 3                   # concurrent runs from this machine
      max_load_ratio: 0.7           # defer at load1 >= 0.7 x CPUs; unset = no load check
      image:
        dockerfile: ~/images/dev.Dockerfile   # built on the host from stdin
      caches:                       # named volumes, kept between runs
        cargo-registry: /usr/local/cargo/registry
      env:
        LANG: C.UTF-8
  local_root: ~/workspace           # mirrors are laid out relative to this
```

Every key, including `prefix`, `root`, `git_root`, `src_root` and
`image.ref`, is in the [configuration guide](configuration.md#remote-hosts). An
existing mirror tree and image from another tool can be adopted by setting
those names; nothing has to be rebuilt.

The block lives in the user state, so `--home` (or `DOTFILES_HOME`) picks whose
state the commands read. `--config` names a sync config, which has no `remote`
block, so `dot remote` refuses it by name rather than addressing the default
state's host while you are reading another file.

### A starter Dockerfile

```dockerfile
FROM debian:12-slim

ARG UID=1000
ARG GID=1000

RUN apt-get update \
 && apt-get install -y --no-install-recommends bash ca-certificates git rsync \
 && rm -rf /var/lib/apt/lists/* \
 && groupadd -g "${GID}" dev \
 && useradd -m -u "${UID}" -g "${GID}" -s /bin/bash dev

USER dev
WORKDIR /home/dev
```

`setup` passes your own UID and GID as build arguments, so the unprivileged
user in the image matches you and can read and write the mirror. Add the
toolchains the work needs to this file; the image is the only place they live.

The `USER` line is not optional: a run passes no `--user`, so the job runs as
whatever the image says, and an image without an unprivileged `USER` would write
root-owned files into the mirror that you cannot clean up on the host. `dot
remote setup` inspects the image and refuses one whose user is root (empty,
`root` or `0`), naming this section.

### Use it

```bash
dot remote setup                     # check the host, build or pull the image
dot remote setup --json              # the same report as one JSON document
dot remote run -- make test          # mirror the current checkout, run there
dot remote run ~/workspace/work/dev/one -- go test ./...
dot remote status                    # load, active runs, mirror sizes, image age
```

`setup --json` keeps stdout to the report alone: the runtime's own build and
pull progress goes to stderr there, so a wrapper can decode the whole output as
one document. `status` does the same, and it reports `sizes_unavailable` with
the host's own reason when it cannot list the record directory, read a record,
or measure a mirror, rather than showing a clean empty list.

`dot remote run` takes the checkout as an optional path (default: the current
directory, which may be a worktree) and a command after `--`. One command
argument is a shell script, several are an argv. The mirror is mounted at
`/work/<path relative to remote.local_root>`, caches are mounted from the
host's volumes, and build outputs stay there between runs.

What leaves the mirror is decided from what this machine sent, not from the
ignore files. A run records the paths it sent to each mirror
(`~/.local/state/dot/remote-sent/<host>--<mirror key>.sent`), and the next run
deletes exactly the recorded paths that are gone from the checkout, so a build
output the host made is never a candidate and neither is a file another machine
sent. rsync reads those ignore files as plain excludes and cannot express a `!`
negation, so they could not decide this: with `*.lock` and `!Cargo.lock` in one
of them, a `Cargo.lock` that was deleted here stayed on the mirror. The record
also names the mirror it describes - ssh destination, remote root, relative
path and the local checkout - and one written for another mirror is ignored
rather than acted on. A record that cannot be written makes the older one
unusable rather than leaving it: the run discards it, empties it when the state
directory refuses the removal, and says which happened, because the next run
then reconciles the mirror instead of subtracting from a listing this run did
not send. When both are refused it names the record file to delete before the
next run, since nothing the run does can make that run reconcile.

A mirror this machine has no record of, because it has never sent there or
because the host's `root` or `ssh` changed, is reconciled once against its own
contents: a run lists what the mirror holds (files and symlinks, never following
a link), keeps every path the checkout still has on disk, and deletes the rest
except what `git check-ignore` reports as ignored, so an adopted mirror loses the
tree an older checkout left while its own build outputs (`node_modules`, `target`
and the rest of the built-in excludes are not even walked) stay. A path git still
lists but this machine no longer has goes too, since `rm old.go` without staging
leaves it listed while the ship pass skips it. If that listing cannot be read,
the run deletes nothing there, records nothing, and says so: the next run
reconciles the mirror again, because a record written there would describe a
mirror this machine never read and the file it missed would never be a candidate
again. A path that is still on this machine is never deleted on the mirror,
whether git lists it or not: `git rm --cached` on a build output leaves it here
and the host's copy stays with it.

The two passes run deletes first, and the send pass carries `--force`, because a
checkout can turn a directory into a file or a symlink: rsync cannot put a
non-directory where the mirror still has a non-empty directory, and it used to
fail there before anything removed the old tree, on that run and every one after
it. A directory the checkout replaced therefore goes whole, including anything
the host built inside it; nothing outside the path being replaced is touched.
Before the passes, the run removes such a directory on the host by name and
reports it: an excluded name inside it (`target`, `node_modules`) is protected
from rsync on the receiving side too, so `--force` alone could not clear it. The
run asks only about files it did not send under that name last time, so an
ordinary run asks about nothing. It asks only about paths the send pass would
really send, which it learns from a dry run of rsync under the same exclude
list, and on the host it does not follow a symlink in the path, so nothing
outside the mirror and nothing the run would not send is removed; a mirror that
is itself a symlink, or sits under one, is left to rsync alone. A name that dry
run cannot print as itself, because it holds a backslash or a control character,
is asked about in a dry run of its own: such a name is told apart from an
excluded one instead of being read as excluded. A file under a directory the
exclude list names stays home even when git tracks it: the directories above
each listed file go through the same dry run, and the run says how many files it
left. A file git still lists under a directory that
is now a file here (the replacement is not staged yet) is left out of the send
list, since rsync cannot read a path under a file. A directory the checkout
deleted outright loses the files it sent, while a build
output the host made under it stays, and the pass reports rsync's "some files
vanished" as the answer it asked for rather than as a failure. A path that is
not a checkout gets one plain `--delete` pass instead of the two, and it meets
the same refusal: the directory rsync named goes by name the same way, once that
path is a file or a symlink in the checkout, and the pass runs once more. A name
rsync printed in its escaped form is not removable that way, so such a directory
is left for `--dry-run` and a manual `rm`. A pass that fails reports the
diagnostic lines above rsync's summary (five at most, never a page of them) and
names what to do next: `--dry-run` prints the plan the pass would have sent.

### Verify a commit

```bash
dot remote verify                    # the project's configured gate on HEAD
dot remote verify --dirty            # the working tree as it is, not HEAD
dot remote verify ~/workspace/work/dev/one -- make check
```

`verify` pushes the checkout's exact commit to an owner-only bare repository on
the host (`<git_root>/<repo>-<hash>.git`, one per repository and shared by its
worktrees, created with mode 700) and runs the command in a fresh checkout of
that commit, with the base branch fetched beside it as `origin/<branch>`. The
real history is there, so a gate that diffs against `main` or checks commit
messages runs unchanged. Without a command it runs the matching
`remote.projects` entry's `verify`; the base is that entry's `base`, or the
remote's default branch. The pushed ref is deleted when the job ends, whatever
its outcome.

`--dirty` stages the working tree (tracked files plus untracked ones no ignore
rule covers) in a throwaway index and writes a commit object on top of HEAD:
your index, branches and refs are not touched, and ignored files are never
sent. Only commits travel; nothing else of the checkout is copied.

The container starts as root only to make the checkout and hand it to your host
UID and GID, then drops to that user with no capabilities before the command
runs. The bare repository is mounted read-only, and the host's caches are
mounted as for a run. `verify` and `run` of one checkout hold the same path
lease, so they exclude each other; the host's `max_runs` and load check apply
as for a run.

### Third-party source

```bash
dot remote src -- git clone https://example.com/lib.git lib
dot remote src lib -- rg -n unsafe
```

`src` runs the command in the host's `src_root` (default `~/dot-remote-src`), or
in the clone `src_root/NAME`, so untrusted code is cloned and analyzed only on
the host. No cache volume is mounted, so that code cannot reach the caches
trusted builds reuse. A NAME is one path segment (letters, digits, `.`, `_`,
`-`); one the host has no clone of exits 66. Each NAME holds its own lease plus
one of the host's `max_runs`.

### Limits and interrupts

A run holds two admission leases, both visible in `dot admit status`: one per
mirror path, so a repository and each of its worktrees run one job at a time,
and one per host, up to `max_runs` from this machine. A run defers with exit
75 (`EX_TEMPFAIL`) and prints the reason, the scope and a retry hint when a
lease is held or the host's own load is at or over `max_load_ratio`. An
unreadable host defers too: missing telemetry is not health.

- `--wait 5m` retries a held lease for up to that long before deferring.
- `DOT_ADMISSION_IGNORE=remote-load` (or a `dot admit relax --ignore
  remote-load` override) steps past the load check for one run. It still reads
  the host and prints the load it found. `slot` in the same list takes a lease
  past capacity, the way `dot admit` treats it; it relaxes the host's
  `max_runs` only, never the one-run-per-path lease, because a second run would
  delete files inside a mirror another run is using.
- `--dry-run` prints the mirror, the container and the run command without
  sending anything.
- `-p PORT` (repeatable) publishes the container's port on the host's
  `127.0.0.1` only and forwards local `127.0.0.1:PORT` to it over the run's own
  ssh connection, so a dev server in the container is reachable here for the
  life of the run. ssh runs with `ExitOnForwardFailure`, so a port already taken
  locally fails the run instead of running it without the forward.
- `--cgroup` gives the command a writable cgroup v2 subtree, its path in
  `DOT_REMOTE_CGROUP`, for tools that create their own cgroups. The container
  starts as root with `CAP_SYS_ADMIN`, AppArmor unconfined and a private cgroup
  namespace only for the setup (an init leaf, the memory controller, a
  delegated subtree owned by the image's user), then `setpriv` drops to that
  user with every capability removed before the command runs. The host must use
  cgroup v2; a failed setup says so and stops before the command.

Interrupting a run removes its container, and so does an ssh connection that
drops (OpenSSH's own status 255, which a command that exits 255 also reports):
the container may still be running there. Either path reports what the removal
achieved, and a container that cannot be removed is `could not remove` with the
claim kept, not a removal that did not happen.

One run per mirror is enforced on the host, not by a check. Before it syncs, a
run takes a claim directory, `<root>/.dot-remote/<mirror key>.claim`, whose
`owner` file names the machine, the pid, the start time and the local checkout;
it removes the claim on every exit path, including an interrupt and a dropped
connection, unless a container removal could not be confirmed. The `mkdir` is
atomic, so two machines racing the same mirror cannot both get in.

- A claim naming another machine, or another checkout, defers the run with exit
  75 and quotes that owner record. Wait for it, or, once you know that run is
  gone, remove it:
  `ssh <host> 'rm -rf "$HOME"/<root>/.dot-remote/<mirror key>.claim'`. The remote
  part is one single-quoted argument so `$HOME` is the host's: pasted unquoted,
  your own shell would expand it and name a path in your local home.
- A claim naming this machine *and* this checkout is a leftover: the local path
  lease this run holds proves the run that took it is dead, so the claim is
  replaced and the container under the mirror's name is removed. A SIGKILLed run
  therefore heals on the next run of the same checkout, from the same machine. A
  second clone of your own is a different checkout, so its claim defers like
  anyone else's.
- A container still running under the mirror's name without a claim (an older
  dot, or a machine that never wrote one) defers the same way; one left stopped
  is removed before the sync.
- A runtime that cannot answer `ps` stops the run instead of reading as "no
  container": the reason names the runtime's own error, nothing is synced and
  nothing is removed.
- A container that cannot be removed stops the run with `could not remove`, and
  the claim stays on the host: that mirror still has a container in it, so
  releasing the claim would let another machine into it. Your next run of the
  same checkout takes the claim over and retries, and the message carries the
  two `ssh` commands that clear both by hand. This holds on the interrupt and
  ssh-255 paths too, where the removal is the last thing a run does. A removal
  the runtime answers with "no such container" is not a failure: the container
  is started with `--rm` and may already be gone, so the run asks whether the
  name is still listed and reports a failed removal only when it is.

Cross-machine `max_runs` is not enforced: two machines pointing at one host can
each start `max_runs` jobs. The one run per mirror is, because the claim and the
container name both come from the mirror's path on that host rather than from
your local path. A claim left by another machine is reported rather than
reclaimed, since nothing here can prove that run is gone. See the ceiling in
[CEILINGS](../CEILINGS.md).

### Remote jobs

`dot remote job` is the file-in, file-out side of the same hosts: named input
files go up, a command runs in an isolated job directory in a profile's own
container, and the files it wrote come back. When the host cannot take the job
the same command runs on this machine under `dot admit`. Nothing about it is
specific to one tool; a profile is config only.

```yaml
remote:
  hosts:
    buildbox:
      # ... the host keys above ...
      jobs:
        media:                        # --profile defaults to the only one
          root: /data/me/jobs         # absolute, or relative to the remote $HOME
          image:
            dockerfile: ~/images/media.Dockerfile   # or ref:
          cpus: 16
          memory: 32g
          max_jobs: 1                 # concurrent jobs of this profile from this machine
          max_load_ratio: 0.7
          min_free_gb: 50             # free space required under root
          env:
            WORKER_THREADS: "{half_cpus}"   # {cpus} and {half_cpus} are filled in
          fallback: local             # local (default) | none
          local:
            root: ~/scratch/jobs      # default ~/.local/state/dot/remote-jobs/<profile>
            path: [~/tools/qa/.venv/bin]
            env: {QA_OFFLINE: "1"}
```

A starter media profile image (unprivileged user, as for the dev image):

```dockerfile
FROM debian:12-slim
ARG UID=1000
ARG GID=1000
RUN apt-get update \
 && apt-get install -y --no-install-recommends bash ca-certificates ffmpeg \
 && rm -rf /var/lib/apt/lists/* \
 && groupadd -g "${GID}" dev \
 && useradd -m -u "${UID}" -g "${GID}" -s /bin/bash dev
USER dev
```

```bash
dot remote setup --profile media     # build or pull the profile's image
dot remote job check                 # exit 0 if the host can take a job now, 69 if not
dot remote job run -o renders -i clip.wav -- 'ffmpeg -i in/clip.wav out/clip.mp3'
dot remote job run --remote-only -o out -i a.wav -- ./qa.sh   # 69 instead of running here
dot remote job run --local-only -o out -- make-thumbs         # skip the host
```

A built profile image is `<prefix>-<profile>:latest`, next to the dev image.
Write the command with relative paths (`in/...`, `out/...`): it runs with the
job directory as its working directory, on the host or here. Inputs are a flat
list of files whose names must be unique, and a missing one fails before
anything is sent. `--name` (default a timestamp) must match
`[a-z0-9][a-z0-9-]{0,63}`.

- `check` is one ssh round trip, 30 seconds at most with a 5-second connect: the
  profile's image is present and not root, load1 is under `max_load_ratio` x
  CPUs, and more than `min_free_gb` is free under `root`. A timeout or an
  unreadable value counts as unavailable.
- On the host, inputs go up with rsync into `<root>/<name>/in/`; the container
  mounts the job directory at `/job`, runs as the image's user with the
  profile's CPU and memory limits and no cache volume, and `out/` comes back
  with rsync into the `-o` directory (created if missing; files with the same
  names are overwritten, others left alone). Only `in/` goes up and only `out/`
  comes back.
- The job directory is deleted when the command and the collection both
  succeed; otherwise it is kept and its location printed. An interrupt removes
  the container by name.
- When `check` fails and the profile's `fallback` is `local` (and no
  `--remote-only`), the command runs here in `<local.root>/<name>` with `in/`
  holding symlinks to the inputs, `local.path` ahead of `PATH` and `local.env`
  set, through `dot admit` as an ordinary heavy job (the pressure gate and the
  local slot apply). stderr says it ran locally and why; `out/` is copied to
  the `-o` directory, and the directory is removed on success and kept on
  failure.
- One lease per profile and host, capacity `max_jobs`, is taken before `check`
  and held through collection or the local run; it shows in `dot admit status`
  and `dot remote status`.

Exit status: the command's own; 75 when the profile's `max_jobs` are already
active from this machine; 69 when the host cannot take the job and
`--remote-only` (or `fallback: none`) keeps it off this machine; 128+signal
when interrupted.

### Where it writes, and cleanup

Mirrors live under `~/dot-remote` on the host (configurable), one directory per
checkout, and `<root>/.dot-remote` holds one small record per mirror so `dot
remote status` can size a mirror whose path is nested. `verify` keeps one bare
repository per repository under `~/dot-remote-git`, and `src` keeps clones
under `~/dot-remote-src`. Images, cache volumes and
containers carry the host's `prefix`, so two people can share one host without
colliding. Nothing else on the host is written, and on this machine a run adds
only the admission leases and the sent listing of that mirror, under
`~/.local/state/dot/remote-sent/`.

To reclaim space, run `dot remote prune`: it lists mirrors whose local
checkout (`remote.local_root/<path>`) no longer exists, verify refs a killed
verify left, and containers carrying the host's prefix that are not running,
and removes them only with `--yes`. A mirror a run holds (its claim on the host,
or a lease here) is skipped, and each removal takes the mirror's claim itself,
so a run that starts meanwhile defers rather than syncing into a directory being
deleted. A verify ref is left while its verify container still runs, and a
container that was restarted since the listing is left too. Job directories
kept after a failed `dot remote job` are listed but never removed: their `out/`
may be the only copy of what the job made, so read and delete them yourself.
The caches stay warm in their volumes; prune does not touch them.

## GPU Server Provisioning

On a fresh DGX or GPU server — auto-detects NVIDIA GPU + CUDA:

```bash
curl -fsSL https://raw.githubusercontent.com/STAIxBWLB/dotfiles/main/scripts/install.sh | bash
dot init --yes     # auto-selects 'server' profile
dot apply --yes    # packages (incl. rsync), shell, git, ssh, terminal, tmux, ai, conda
```

Or import config from your workstation:

```bash
dot init --from ~/workspace/secrets/dotfiles-config.yaml
dot apply --yes
```

Detection: `nvidia-smi` (GPU model), `/usr/local/cuda` (CUDA home), `/etc/dgx-release` (DGX).

---

## Tunnel operations

`dot tunnel setup` renders explicit `--config /etc/cloudflared/config.yml tunnel run` arguments, avoiding the macOS service-install form that omits the run command. Cloudflare Access policy creation stays in the dashboard; then run `dot tunnel client add <hostname>` on clients. `dot tunnel status`, `log` and `uninstall` cover the server side, and `dot tunnel client list` and `remove` the client side ([command reference](../commands/dot_tunnel.md)).
