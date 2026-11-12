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

Interrupting a run removes its container, and so does an ssh connection that
drops (OpenSSH's own status 255, which a command that exits 255 also reports):
the container may still be running there.

One run per mirror is enforced on the host, not by a check. Before it syncs, a
run takes a claim directory, `<root>/.dot-remote/<mirror key>.claim`, whose
`owner` file names the machine, the pid, the start time and the local checkout;
it removes the claim on every exit path, including an interrupt and a dropped
connection. The `mkdir` is atomic, so two machines racing the same mirror cannot
both get in.

- A claim naming another machine, or another checkout, defers the run with exit
  75 and quotes that owner record. Wait for it, or, once you know that run is
  gone, remove it: `ssh <host> rm -rf <root>/.dot-remote/<mirror key>.claim`.
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
  two `ssh` commands that clear both by hand.

Cross-machine `max_runs` is not enforced: two machines pointing at one host can
each start `max_runs` jobs. The one run per mirror is, because the claim and the
container name both come from the mirror's path on that host rather than from
your local path. A claim left by another machine is reported rather than
reclaimed, since nothing here can prove that run is gone. See the ceiling in
[CEILINGS](../CEILINGS.md).

### Where it writes, and cleanup

Mirrors live under `~/dot-remote` on the host (configurable), one directory per
checkout, and `<root>/.dot-remote` holds one small record per mirror so `dot
remote status` can size a mirror whose path is nested. Images, cache volumes and
containers carry the host's `prefix`, so two people can share one host without
colliding. Nothing else on the host is written, and this machine only gains
admission leases.

To reclaim space, delete the mirror directory of a checkout you no longer use;
the next run of that path recreates it, and the caches stay warm in their
volumes. There is no prune command yet: the mirror tree is made of ordinary
directories.

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
