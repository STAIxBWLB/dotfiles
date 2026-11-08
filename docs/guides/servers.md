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

`dot tunnel setup` renders explicit `--config /etc/cloudflared/config.yml tunnel run` arguments, avoiding the macOS service-install form that omits the run command. Cloudflare Access policy creation stays in the dashboard; then run `dot tunnel client add <hostname>` on clients.
