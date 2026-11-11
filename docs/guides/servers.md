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

### Use it

```bash
dot remote setup                     # check the host, build or pull the image
dot remote run -- make test          # mirror the current checkout, run there
dot remote run ~/workspace/work/dev/one -- go test ./...
dot remote status                    # load, active runs, mirror sizes, image age
```

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
  past capacity, the way `dot admit` treats it.
- `--dry-run` prints the mirror, the container and the run command without
  sending anything.

Interrupting a run removes its container. A SIGKILL cannot, so the next run of
that path removes the container left behind before it starts.

Cross-machine `max_runs` is not enforced: two machines pointing at one host can
each start `max_runs` jobs. See the ceiling in [CEILINGS](../CEILINGS.md).

### Where it writes, and cleanup

Mirrors live under `~/dot-remote` on the host (configurable), one directory per
checkout. Images, cache volumes and containers carry the host's `prefix`, so
two people can share one host without colliding. Nothing else on the host is
written, and this machine only gains admission leases.

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
