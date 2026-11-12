#!/usr/bin/env bash
# Scenario: dot remote end-to-end, one machine, real ssh loopback, stub runtime
#
# What is under test is the whole path a person's build takes: the config block,
# the admission leases, the real two-pass rsync over ssh, and the container argv
# the runtime receives. The container runtime is a stub on the remote PATH, so
# no image and no daemon is needed; everything above it is real. The "host" is
# this container, reached over ssh to 127.0.0.1, which is what makes the mirror
# and the leases land somewhere the assertions can look at.
set -euo pipefail
# shellcheck source=tests/assert.sh disable=SC1091
source "$(dirname "$0")/../assert.sh"

# assert.sh carries no generic pass/fail. Both append to ERRORS so the terminal
# report keeps the detail rather than only the count.
pass() {
  PASS=$((PASS + 1))
  echo "  ✓ $1"
}
fail() {
  FAIL=$((FAIL + 1))
  ERRORS+=("FAIL: $1")
  echo "  ✗ $1"
}

# ABORT records a fixture failure the rest of the run cannot proceed past: a
# half-built fixture turns every later assertion into noise, all naming the
# wrong cause.
ABORT() {
  fail "$1"
  report || true
  exit 1
}

echo "=== Scenario: remote ==="

BIN="${1:-}"
if [ -z "$BIN" ] || [ ! -x "$BIN" ]; then
  for candidate in ./bin/dot ./dot /usr/local/bin/dot "$(command -v dot 2>/dev/null)"; do
    if [ -n "$candidate" ] && [ -x "$candidate" ]; then
      BIN="$candidate"
      break
    fi
  done
fi
if [ -z "$BIN" ] || [ ! -x "$BIN" ]; then
  ABORT "fixture: the dot binary was not found"
fi

# Non-default so the daemon cannot collide with a future layer's sshd.
SSH_PORT=2223
SSHD_BIN=/usr/sbin/sshd
PIDFILE=/run/sshd/ssh-remote.pid

tmpdir=$(mktemp -d)
# Single-quoted so the expansions happen at trap time (SC2064).
cleanup() {
  if [ -f "$PIDFILE" ]; then
    sudo kill "$(sudo cat "$PIDFILE")" >/dev/null 2>&1 || true
  fi
  rm -rf "$tmpdir" "$HOME/remote-work" "$HOME/dot-remote" "$HOME/dot-remote-git" "$HOME/dot-remote-src" \
    "$HOME/bin/docker" "$HOME/stub-hold" "$HOME/stub.log" "$HOME/stub-containers" "$HOME/stub-running" \
    "$HOME/stub-remove-fail" "$HOME/e2e-jobs" "$HOME/e2e-jobs-full" "$HOME/e2e-local-jobs" \
    "$HOME/.config/dotfiles/config.yaml"
}
trap 'cleanup' EXIT

# ── fixture gates ────────────────────────────────────────────────────────────
# Any of these missing is a fixture failure, not a skip: this scenario runs only
# in the linux image, which guarantees every binary it needs.
for tool in git rsync ssh ssh-keygen; do
  if command -v "$tool" >/dev/null 2>&1; then
    pass "fixture: $tool is on PATH"
  else
    ABORT "fixture: $tool is not in the image"
  fi
done
if [ -x "$SSHD_BIN" ]; then
  pass "$SSHD_BIN is executable"
else
  ABORT "fixture: $SSHD_BIN is not executable (openssh-server missing from the image)"
fi
# The two-pass sync needs 3.2.3 on both ends; openrsync is refused by design.
# The version is read whole: `rsync --version | head -1` makes rsync die of
# SIGPIPE as soon as head exits, and under pipefail that 141 is the pipeline's
# status, so a perfectly modern rsync would read as unfit. awk reads to EOF and
# keeps the first line instead.
rsync_version=$(rsync --version 2>/dev/null | awk 'NR == 1 { first = $0 } END { print first }')
rsync_numbers=$(printf '%s\n' "$rsync_version" | sed -n 's/.*version \([0-9][0-9]*\)\.\([0-9][0-9]*\)\.\([0-9][0-9]*\).*/\1 \2 \3/p')
if [ -z "$rsync_numbers" ]; then
  ABORT "fixture: rsync reports no rsync 3.x version line (openrsync is refused by design): $rsync_version"
fi
read -r rsync_major rsync_minor rsync_patch <<<"$rsync_numbers"
if [ "$((10#$rsync_major * 10000 + 10#$rsync_minor * 100 + 10#$rsync_patch))" -ge 30203 ]; then
  pass "the image's rsync is 3.2.3 or newer: $rsync_version"
else
  ABORT "fixture: the image's rsync is too old for the two-pass sync (3.2.3 is the floor): $rsync_version"
fi

# ── the runtime stub ─────────────────────────────────────────────────────────
# Named docker, because the config may only name docker or podman. It runs on
# the far side of ssh, so the paths are baked in: no environment crosses ssh
# without ServerAlivePermitUserEnvironment.
STUB="$HOME/bin/docker"
mkdir -p "$HOME/bin"
cat >"$STUB" <<'STUB_SCRIPT'
#!/usr/bin/env bash
# Stands in for the container runtime: records the argv, keeps the set of
# containers that exist, and reports success. A run asks the host about its
# mirror's container and its image's user before it sends anything, so `ps` and
# `image inspect` answer from that set rather than from a daemon.
log="__LOG__"
containers="__CONTAINERS__"
running="__RUNNING__"
printf '%s\n' "$*" >>"$log"

# filter_name strips "name=^" and the trailing "$" from a --filter value.
filter_name() {
  want=${1#name=^}
  printf '%s' "${want%\$}"
}

forget() {
  for file in "$containers" "$running"; do
    grep -vx -- "$1" "$file" >"$file.next" || true
    mv "$file.next" "$file"
  done
}

case "$1" in
  --version) echo "Docker version e2e-stub, build 0" ;;
  run)
    name=""
    previous=""
    for arg in "$@"; do
      if [ "$previous" = "--name" ]; then name="$arg"; fi
      previous="$arg"
    done
    if [ -n "$name" ]; then
      printf '%s\n' "$name" >>"$containers"
      printf '%s\n' "$name" >>"$running"
    fi
    if [ -f "$HOME/stub-hold" ]; then
      sleep "$(cat "$HOME/stub-hold")"
    fi
    # A job mounts its directory at /job: run its command there for real, so
    # the round trip of in/ and out/ is what the assertions read.
    jobdir=""
    jobenv=()
    previous=""
    for arg in "$@"; do
      if [ "$previous" = "-v" ] && [ "${arg%:/job}" != "$arg" ]; then jobdir="${arg%:/job}"; fi
      if [ "$previous" = "-e" ]; then jobenv+=("$arg"); fi
      previous="$arg"
    done
    if [ -n "$jobdir" ]; then
      script="${*: -1}"
      set +e
      (cd "$jobdir" && env "${jobenv[@]}" bash -c "$script")
      code=$?
      if [ -n "$name" ]; then forget "$name"; fi
      exit "$code"
    fi
    echo "stub-container-ran"
    # --rm: nothing is left once the command returns.
    if [ -n "$name" ]; then forget "$name"; fi
    ;;
  rm)
    # stub-remove-fail makes the runtime refuse a removal, which is what a run
    # that keeps its claim has to notice.
    if [ -f "$HOME/stub-remove-fail" ]; then
      echo "Error response from daemon: container is in use" >&2
      exit 1
    fi
    name=""
    previous=""
    for arg in "$@"; do
      if [ "$previous" = "-f" ]; then name="$arg"; fi
      previous="$arg"
    done
    if [ -n "$name" ]; then forget "$name"; fi
    ;;
  ps)
    # -a lists stopped containers too; without it only running ones answer.
    file="$running"
    filter=""
    previous=""
    for arg in "$@"; do
      if [ "$arg" = "-a" ]; then file="$containers"; fi
      if [ "$previous" = "--filter" ]; then filter="$arg"; fi
      previous="$arg"
    done
    name=$(filter_name "$filter")
    if [ -n "$name" ] && [ -f "$file" ] && grep -qx -- "$name" "$file"; then
      printf '%s\n' "$name"
    fi
    ;;
  image)
    if printf '%s' "$*" | grep -q 'Created'; then
      echo '2026-01-01T00:00:00Z'
    else
      echo '1000:1000'
    fi
    ;;
esac
exit 0
STUB_SCRIPT
sed -i -e "s|__LOG__|$HOME/stub.log|" -e "s|__CONTAINERS__|$HOME/stub-containers|" \
  -e "s|__RUNNING__|$HOME/stub-running|" "$STUB"
chmod +x "$STUB"

# ── sshd on loopback ─────────────────────────────────────────────────────────
sudo mkdir -p /run/sshd
sudo ssh-keygen -A >/dev/null

# UsePAM=no has a consequence beyond skipping jammy's pam_loginuid.so: sshd
# then runs its own account check, `allowed_user()`, which treats a shadow field
# equal to `*` or beginning with `!` as a locked account and refuses the login
# before pubkey auth is attempted - the client shows only "Permission denied
# (publickey)" and nothing says why. The image's `useradd -m -s /bin/bash
# testuser` grants no password, so the field is `!`. `*NP*` is neither of those
# forms and no crypt(3) output can equal it, so no password authenticates;
# PasswordAuthentication=no keeps that property regardless.
sudo usermod -p '*NP*' "$(id -un)"

# Port before ListenAddress: sshd applies a bare ListenAddress against the Port
# directives seen so far. One array, so the daemon and the `-T` dump below are
# the same configuration.
#
# UsePAM=no          jammy's /etc/pam.d/sshd has pam_loginuid.so as `required`
#                    and it fails in a container. Throwaway daemon in an
#                    ephemeral container; do not copy this into a real one.
# PasswordAuthentication=no  public key only.
# PermitRootLogin=no this user is not root.
# ListenAddress=127.0.0.1  the socket must not leave the container.
# SetEnv=PATH        a non-login, non-interactive shell gets sshd's own PATH,
#                    which does not carry the stub's directory.
# -E                 sshd's own log, so a refused login says why instead of
#                    leaving the client's one line as the only evidence.
SSHD_LOG="$tmpdir/sshd.log"
SSHD_OPTS=(
  -o "Port=$SSH_PORT"
  -o UsePAM=no
  -o PasswordAuthentication=no
  -o PermitRootLogin=no
  -o ListenAddress=127.0.0.1
  -o "PidFile=$PIDFILE"
  -o "SetEnv=PATH=$HOME/bin:/usr/local/bin:/usr/bin:/bin"
  -E "$SSHD_LOG"
)
sudo "$SSHD_BIN" "${SSHD_OPTS[@]}"

# sshd reads AuthorizedKeysFile and StrictModes from its effective config, not
# from what this script meant to pass, so both are read back here. The path is
# absolute and named by the dump, which is also what makes an sshd_config that
# overrides AuthorizedKeysFile unable to hide.
sshd_effective=$(sudo "$SSHD_BIN" -T "${SSHD_OPTS[@]}" 2>&1 |
  grep -Ei '^(authorizedkeysfile|strictmodes|usepam|pubkeyauthentication|passwordauthentication)' |
  tr '\n' ';')

# sshd resolves the login user's home from passwd, not from this script's $HOME,
# so the key goes where that user's entry points. It is the same directory in
# this image; naming it keeps the two from drifting apart silently.
login_home=$(getent passwd "$(id -un)" | cut -d: -f6)
if [ -z "$login_home" ]; then
  ABORT "fixture: $(id -un) has no home in passwd, so sshd has nowhere to read authorized_keys from"
fi

sshd_diagnostics() {
  echo "  sshd effective: $sshd_effective"
  echo "  login home: $login_home (this script's \$HOME is $HOME)"
  echo "  modes: $(ls -ld "$login_home" "$login_home/.ssh" "$login_home/.ssh/authorized_keys" 2>&1 | tr '\n' ';')"
  echo "  passwd: $(getent passwd "$(id -un)")"
  echo "  shadow field: '$(sudo getent shadow "$(id -un)" 2>/dev/null | cut -d: -f2)'"
  echo "  sshd log: $(sudo tail -20 "$SSHD_LOG" 2>&1 | tr '\n' ';')"
}

sshd_ready=""
attempt=0
while [ "$attempt" -lt 20 ]; do
  if (: </dev/tcp/127.0.0.1/"$SSH_PORT") >/dev/null 2>&1; then
    sshd_ready=yes
    break
  fi
  attempt=$((attempt + 1))
  sleep 0.5
done
if [ -z "$sshd_ready" ]; then
  sshd_diagnostics
  ABORT "fixture: sshd never accepted a connection on port $SSH_PORT"
fi

ssh-keygen -t ed25519 -N '' -C ssh-remote-scenario -f "$tmpdir/id_ed25519" >/dev/null
sudo install -d -m 700 -o "$(id -un)" -g "$(id -gn)" "$login_home/.ssh"
sudo install -m 600 -o "$(id -un)" -g "$(id -gn)" "$tmpdir/id_ed25519.pub" "$login_home/.ssh/authorized_keys"

# An ssh alias is what the config block names, so the scenario exercises the
# same indirection a person's `Host buildbox` entry uses.
mkdir -p "$HOME/.ssh"
cat >"$HOME/.ssh/config" <<EOF
Host remotehost
  HostName 127.0.0.1
  Port $SSH_PORT
  User $(id -un)
  IdentityFile $tmpdir/id_ed25519
  IdentitiesOnly yes
  BatchMode yes
  StrictHostKeyChecking no
  UserKnownHostsFile $tmpdir/known_hosts
  LogLevel ERROR
EOF
chmod 600 "$HOME/.ssh/config"

SSH_ERR="$tmpdir/ssh.err"
ssh_verbose="$tmpdir/ssh.verbose"
remote_user=$(ssh remotehost 'id -un' 2>"$SSH_ERR" || true)
if [ "$remote_user" = "$(id -un)" ]; then
  pass "ssh to the alias works and answers as $(id -un)"
else
  sshd_diagnostics
  # A second attempt with -v, only on failure: the config's LogLevel ERROR keeps
  # the success path quiet, and what a refused login needs is the client's own
  # account of what it offered and what the server said.
  ssh -v remotehost 'id -un' >/dev/null 2>"$ssh_verbose" || true
  echo "  ssh -v: $(grep -Ei 'offering|server accepts|authentications that can continue|denied|error' "$ssh_verbose" | tail -12 | tr '\n' ';')"
  ABORT "fixture: ssh remotehost failed: got '$remote_user' [ssh: $(tr '\n' ' ' <"$SSH_ERR")]"
fi
remote_docker=$(ssh remotehost 'command -v docker' 2>"$SSH_ERR" || true)
if [ "$remote_docker" = "$STUB" ]; then
  pass "a non-interactive ssh session resolves the stub runtime at $STUB"
else
  sshd_diagnostics
  ABORT "fixture: the ssh session's PATH does not reach the stub: got '$remote_docker' [ssh: $(tr '\n' ' ' <"$SSH_ERR")]"
fi

# ── the workspace and its config ─────────────────────────────────────────────
mkdir -p "$HOME/remote-work/one" "$HOME/remote-work/two"
cat >"$HOME/remote-work/one/.gitignore" <<'EOF'
build/
EOF
printf 'hello from the checkout\n' >"$HOME/remote-work/one/hello.txt"
printf 'deleted locally later\n' >"$HOME/remote-work/one/gone.txt"
printf 'printf ok\n' >"$HOME/remote-work/two/other.txt"
for repo in one two; do
  git -C "$HOME/remote-work/$repo" -c init.defaultBranch=main init --quiet .
done

mkdir -p "$HOME/.config/dotfiles"
cat >"$HOME/.config/dotfiles/config.yaml" <<EOF
remote:
  default: remotehost
  local_root: $HOME/remote-work
  hosts:
    remotehost:
      prefix: e2e-rdev
      cpus: 2
      memory: 1g
      max_runs: 1
      image:
        ref: example.invalid/e2e:1
    loaded:
      ssh: remotehost
      prefix: e2e-loaded
      image:
        ref: example.invalid/e2e:1
      max_load_ratio: 0.000001
    jobhost:
      ssh: remotehost
      prefix: e2e-job
      image:
        ref: example.invalid/e2e:1
      jobs:
        media:
          root: $HOME/e2e-jobs
          image:
            ref: example.invalid/media:1
          cpus: 2
          memory: 1g
          env:
            THREADS: "{half_cpus}"
          local:
            root: $HOME/e2e-local-jobs
        full:
          root: e2e-jobs-full
          image:
            ref: example.invalid/media:1
          min_free_gb: 999999999
          local:
            root: $HOME/e2e-local-jobs
EOF

# ── setup ────────────────────────────────────────────────────────────────────
setup_err="$tmpdir/setup.err"
if "$BIN" remote setup >"$tmpdir/setup.out" 2>"$setup_err"; then
  # The argv the runtime received, not the quoted command string this machine
  # sent: the far side's shell consumes ShellQuote's quotes, so the stub logs
  # `pull example.invalid/e2e:1`. The quotes stay optional here so the
  # assertion does not break if ShellQuote starts quoting a safe ref.
  if grep -qE "^pull '?example\.invalid/e2e:1'?$" "$HOME/stub.log"; then
    pass "setup pulled the configured image through the host's runtime"
  else
    fail "setup did not pull the image: $(cat "$HOME/stub.log" 2>/dev/null)"
  fi
  if grep -q "remotehost is ready" "$tmpdir/setup.out"; then
    pass "setup reports the host ready"
  else
    fail "setup did not report the host ready: $(cat "$tmpdir/setup.out")"
  fi
  # A run gets the image's own user, so setup names it: an image without an
  # unprivileged USER would leave root-owned files in the mirror.
  if grep -q "runs as 1000:1000" "$tmpdir/setup.out"; then
    pass "setup names the unprivileged user a run will be"
  else
    fail "setup did not name the image's user: $(cat "$tmpdir/setup.out")"
  fi
else
  fail "dot remote setup exited non-zero: $(cat "$setup_err")"
fi

# The same report as one machine-readable document, which is what a wrapper
# around dot reads.
if "$BIN" remote setup --json >"$tmpdir/setup.json" 2>/dev/null; then
  if grep -q '"host": "remotehost"' "$tmpdir/setup.json" &&
    grep -q '"image_user": "1000:1000"' "$tmpdir/setup.json" &&
    grep -q '"built": false' "$tmpdir/setup.json"; then
    pass "setup --json prints the report as one JSON document"
  else
    fail "setup --json is not the report: $(cat "$tmpdir/setup.json")"
  fi
else
  fail "dot remote setup --json exited non-zero"
fi

# ── run ──────────────────────────────────────────────────────────────────────
: >"$HOME/stub.log"
run_err="$tmpdir/run.err"
if "$BIN" remote run "$HOME/remote-work/one" -- echo hello >"$tmpdir/run.out" 2>"$run_err"; then
  pass "a run of one checkout exited 0"
else
  fail "a run of one checkout failed: $(cat "$run_err")"
fi
run_argv=$(grep '^run --rm ' "$HOME/stub.log" 2>/dev/null || true)
if printf '%s' "$run_argv" | grep -q -- '--name e2e-rdev-'; then
  pass "the runtime received the container name derived from the path"
else
  fail "the runtime argv has no derived container name: '$run_argv'"
fi
if printf '%s' "$run_argv" | grep -q " -w /work/one "; then
  pass "the container's working directory is the mirror's relative path"
else
  fail "the container argv has no /work/one workdir: '$run_argv'"
fi
if printf '%s' "$run_argv" | grep -q -- "--cpus 2" && printf '%s' "$run_argv" | grep -q -- "--memory 1g"; then
  pass "the container argv carries the configured CPU and memory limits"
else
  fail "the container argv has no limits: '$run_argv'"
fi
if printf '%s' "$run_argv" | grep -q "bash -c echo hello"; then
  pass "the command reaches the container as one script"
else
  fail "the command did not reach the container: '$run_argv'"
fi
if grep -q 'stub-container-ran' "$tmpdir/run.out"; then
  pass "the container's output comes back to this machine"
else
  fail "the container's output did not come back: $(cat "$tmpdir/run.out")"
fi

MIRROR="$HOME/dot-remote/one"
if [ "$(cat "$MIRROR/hello.txt" 2>/dev/null)" = "hello from the checkout" ]; then
  pass "the mirror holds the checkout's file, byte for byte"
else
  fail "the mirror does not hold hello.txt: $(ls -A "$MIRROR" 2>/dev/null | tr '\n' ' ')"
fi

# What the host built, and what the checkout dropped: the next run has to keep
# the first and remove the second. That is the whole point of the two passes.
mkdir -p "$MIRROR/build"
printf 'built on the host\n' >"$MIRROR/build/out.bin"
rm -f "$HOME/remote-work/one/gone.txt"
if "$BIN" remote run "$HOME/remote-work/one" -- true >/dev/null 2>"$run_err"; then
  pass "the second run of the same checkout exited 0"
else
  fail "the second run failed: $(cat "$run_err")"
fi
if [ -f "$MIRROR/build/out.bin" ]; then
  pass "the gitignored build output on the mirror survived the delete pass"
else
  fail "the delete pass removed the host's build output"
fi
if [ -e "$MIRROR/gone.txt" ]; then
  fail "a file deleted locally is still on the mirror"
else
  pass "a file deleted locally is gone from the mirror"
fi
if [ -e "$MIRROR/.git" ]; then
  fail "the mirror carries a .git directory"
else
  pass "the mirror carries no .git directory"
fi

# The record a run leaves so `status` can size a mirror whose path is nested:
# the name is derived from the mirror's own relative path.
if grep -qx "one" "$HOME/dot-remote/.dot-remote/"* 2>/dev/null; then
  pass "the run recorded the mirror for status"
else
  fail "no record under $HOME/dot-remote/.dot-remote names one"
fi

# ── the container name is the host's, not this machine's ─────────────────────
# Leases are per machine, so a container left by a killed run - or by another
# machine mirroring the same checkout - is the one exclusion the host enforces.
# The name comes from dot's own dry run, so this arm cannot drift from the
# derivation it is checking.
left_container=$("$BIN" remote run --dry-run "$HOME/remote-work/one" -- true 2>/dev/null |
  sed -n 's/.*(container \([^)]*\)).*/\1/p')
if [ -n "$left_container" ]; then
  pass "a dry run names the container of a mirror: $left_container"
else
  fail "a dry run named no container: $(cat "$tmpdir/run.err")"
fi

printf '%s\n' "$left_container" >"$HOME/stub-containers"
printf '%s\n' "$left_container" >"$HOME/stub-running"
set +e
"$BIN" remote run "$HOME/remote-work/one" -- true >"$tmpdir/running.out" 2>"$tmpdir/running.err"
running_rc=$?
set -e
if [ "$running_rc" -eq 75 ]; then
  pass "a container already running under the mirror's name defers with exit 75"
else
  fail "a running container under the mirror's name exited $running_rc, want 75: $(cat "$tmpdir/running.err")"
fi
if grep -q "another machine is running this checkout" "$tmpdir/running.out" "$tmpdir/running.err"; then
  pass "the defer reason names the other machine's run"
else
  fail "the defer reason does not name the other run: $(cat "$tmpdir/running.err")"
fi

# The same container stopped: this run removes it before it syncs rather than
# colliding with it, and says so.
: >"$HOME/stub-running"
: >"$HOME/stub.log"
if "$BIN" remote run "$HOME/remote-work/one" -- true >"$tmpdir/left.out" 2>"$tmpdir/left.err"; then
  pass "a run whose container name is held by a stopped container still runs"
else
  fail "a run with a stopped container under its name failed: $(cat "$tmpdir/left.err")"
fi
if grep -q "removed the stopped container $left_container left on remotehost" "$tmpdir/left.err"; then
  pass "the run removed the stopped container and said so"
else
  fail "the run did not report removing the stopped container: $(cat "$tmpdir/left.err")"
fi
if grep -qx "rm -f $left_container" "$HOME/stub.log"; then
  pass "the runtime was asked to remove it by name"
else
  fail "the runtime was not asked to remove it: $(cat "$HOME/stub.log")"
fi

# ── the host's own exclusion: one claim per mirror ───────────────────────────
# The leases are per machine, so two machines mirroring one checkout hold one
# each and both pass. What excludes them is a directory on the host: mkdir is
# atomic, the owner file names the run, and it is removed on every exit path -
# which is what makes a leftover claim mean a run that died rather than a run
# that is still working.
CLAIMS="$HOME/dot-remote/.dot-remote"
# The key comes from dot's own dry run, so the arm cannot drift from the
# derivation it checks: the container name is <prefix>-<mirror key>.
claim_dir="$CLAIMS/${left_container#e2e-rdev-}.claim"
: >"$HOME/stub.log"
printf '6\n' >"$HOME/stub-hold"
"$BIN" remote run "$HOME/remote-work/one" -- sleep 6 >"$tmpdir/claim.out" 2>"$tmpdir/claim.err" &
claim_pid=$!
claim_owner=""
attempt=0
while [ "$attempt" -lt 40 ]; do
  if [ -f "$claim_dir/owner" ]; then
    claim_owner=$(cat "$claim_dir/owner" 2>/dev/null || true)
    break
  fi
  attempt=$((attempt + 1))
  sleep 0.25
done
if [ -n "$claim_owner" ]; then
  pass "a run claims the mirror on the host before it syncs: $claim_dir"
else
  fail "no claim appeared at $claim_dir while a run held the mirror"
fi
if [ "$(printf '%s' "$claim_owner" | cut -f1)" = "$(uname -n)" ] &&
  [ -n "$(printf '%s' "$claim_owner" | cut -f2)" ] &&
  [ -n "$(printf '%s' "$claim_owner" | cut -f3)" ] &&
  [ "$(printf '%s' "$claim_owner" | cut -f4)" = "$HOME/remote-work/one" ]; then
  pass "the claim names this machine, its pid, when it started and the checkout: $claim_owner"
else
  fail "the claim's owner line is not machine/pid/start/checkout: '$claim_owner'"
fi
set +e
wait "$claim_pid"
claim_rc=$?
set -e
rm -f "$HOME/stub-hold"
if [ "$claim_rc" -eq 0 ]; then
  pass "the claiming run finished on its own"
else
  fail "the claiming run exited $claim_rc: $(cat "$tmpdir/claim.err")"
fi
if [ -e "$claim_dir" ]; then
  fail "the claim outlived the run that took it: $(ls -A "$claim_dir" 2>/dev/null | tr '\n' ' ')"
else
  pass "a finished run releases its claim"
fi

# A claim another machine holds is not this run's to break: it defers, names the
# owner, and hands over the command that clears it once that run is gone.
mkdir -p "$claim_dir"
printf 'another-box\t4242\t2026-01-01T00:00:00Z\t%s\n' "$HOME/remote-work/one" >"$claim_dir/owner"
: >"$HOME/stub.log"
set +e
"$BIN" remote run "$HOME/remote-work/one" -- true >"$tmpdir/foreign.out" 2>"$tmpdir/foreign.err"
foreign_rc=$?
set -e
if [ "$foreign_rc" -eq 75 ]; then
  pass "a claim another machine holds defers the run with exit 75"
else
  fail "a foreign claim exited $foreign_rc, want 75: $(cat "$tmpdir/foreign.err")"
fi
if grep -q "the claim on dot-remote/one on remotehost belongs to another-box" \
  "$tmpdir/foreign.out" "$tmpdir/foreign.err"; then
  pass "the defer reason names the machine that holds the claim"
else
  fail "the defer reason does not name the claim's owner: $(cat "$tmpdir/foreign.err")"
fi
if grep -qF "ssh remotehost 'rm -rf \"\$HOME\"/dot-remote/.dot-remote/" \
  "$tmpdir/foreign.out" "$tmpdir/foreign.err"; then
  pass "the defer reason carries the command that clears the claim, quoted for the host"
else
  fail "the defer reason carries no quoted removal command: $(cat "$tmpdir/foreign.err")"
fi
if grep -q '^run --rm ' "$HOME/stub.log" 2>/dev/null; then
  fail "a deferred run still started a container"
else
  pass "nothing is synced or run past another machine's claim"
fi

# The same claim, now carrying this machine's own name AND this checkout, plus
# the container a killed run left: the local path lease this run holds proves
# that owner is dead, so the leftover is reclaimed instead of reported as
# another machine's forever. A claim naming this machine and another checkout
# would defer here instead, which is what the owner record's fourth field is for.
printf '%s\t99999\t2026-01-01T00:00:00Z\t%s\n' "$(uname -n)" "$HOME/remote-work/one" >"$claim_dir/owner"
printf '%s\n' "$left_container" >"$HOME/stub-containers"
: >"$HOME/stub-running"
: >"$HOME/stub.log"
if "$BIN" remote run "$HOME/remote-work/one" -- true >"$tmpdir/stale.out" 2>"$tmpdir/stale.err"; then
  pass "a claim this machine left behind is taken over rather than deferred"
else
  fail "this machine's own leftover claim stopped the run: $(cat "$tmpdir/stale.err")"
fi
if grep -q "took over the claim an earlier run of" "$tmpdir/stale.err"; then
  pass "the takeover is reported, not silent"
else
  fail "the run took the claim over silently: $(cat "$tmpdir/stale.err")"
fi
if grep -qx "rm -f $left_container" "$HOME/stub.log" &&
  grep -q "removed the container $left_container that run left behind" "$tmpdir/stale.err"; then
  pass "the takeover removes the container that dead run left behind"
else
  fail "the takeover left the dead run's container in place: $(cat "$tmpdir/stale.err")"
fi
if [ -e "$claim_dir" ]; then
  fail "the takeover left the replaced claim behind"
else
  pass "the takeover's own claim is released at the end of the run"
fi

# The same machine name with another checkout in the record is NOT a takeover:
# the local path lease this run holds proves one path's previous run dead, not
# that one, so the run defers like it would for a foreign machine.
mkdir -p "$claim_dir"
printf '%s\t99999\t2026-01-01T00:00:00Z\t%s\n' "$(uname -n)" "$HOME/remote-work/two" >"$claim_dir/owner"
: >"$HOME/stub.log"
set +e
"$BIN" remote run "$HOME/remote-work/one" -- true >"$tmpdir/otherpath.out" 2>"$tmpdir/otherpath.err"
otherpath_rc=$?
set -e
if [ "$otherpath_rc" -eq 75 ]; then
  pass "a claim for another checkout of this machine defers the run"
else
  fail "another checkout's claim exited $otherpath_rc, want 75: $(cat "$tmpdir/otherpath.err")"
fi
if grep -q "belongs to $(uname -n) 99999" "$tmpdir/otherpath.out" "$tmpdir/otherpath.err"; then
  pass "the defer reason quotes the owner record of the other checkout"
else
  fail "the defer reason does not quote the record: $(cat "$tmpdir/otherpath.err")"
fi
if grep -qx "rm -f $left_container" "$HOME/stub.log" 2>/dev/null; then
  fail "a run that deferred removed the container anyway"
else
  pass "another checkout's claim is left untouched, container included"
fi

# A removal the runtime refuses has not freed the name, so the run stops saying
# "could not remove" and keeps its claim: releasing it would invite another
# machine into a mirror that still has that container in it.
printf '%s\n' "$left_container" >"$HOME/stub-containers"
: >"$HOME/stub-running"
printf '%s\t99999\t2026-01-01T00:00:00Z\t%s\n' "$(uname -n)" "$HOME/remote-work/one" >"$claim_dir/owner"
touch "$HOME/stub-remove-fail"
: >"$HOME/stub.log"
set +e
"$BIN" remote run "$HOME/remote-work/one" -- true >"$tmpdir/nofree.out" 2>"$tmpdir/nofree.err"
nofree_rc=$?
set -e
rm -f "$HOME/stub-remove-fail"
if [ "$nofree_rc" -ne 0 ] && grep -q "could not remove the container $left_container" "$tmpdir/nofree.err"; then
  pass "a removal the runtime refuses stops the run with could not remove"
else
  fail "a refused removal exited $nofree_rc without saying it could not remove: $(cat "$tmpdir/nofree.err")"
fi
if grep -q "removed the container" "$tmpdir/nofree.err"; then
  fail "the run reported a removal that did not happen: $(cat "$tmpdir/nofree.err")"
else
  pass "the run does not claim to have removed what is still there"
fi
if [ -f "$claim_dir/owner" ]; then
  pass "the claim stays while its container could not be removed"
else
  fail "the claim was released although its container is still there"
fi
if grep -q '^run --rm ' "$HOME/stub.log" 2>/dev/null; then
  fail "a run whose container could not be removed still started"
else
  pass "nothing was synced or run past a container the runtime would not remove"
fi

# ── leases: one per path, one per host ───────────────────────────────────────
# The stub holds the first run open, so the second one meets a held lease. The
# log starts empty: the wait below looks for a `run --rm` line, and an earlier
# arm's line would satisfy it before this run had started.
: >"$HOME/stub.log"
printf '6\n' >"$HOME/stub-hold"
"$BIN" remote run "$HOME/remote-work/one" -- sleep 6 >"$tmpdir/hold.out" 2>"$tmpdir/hold.err" &
hold_pid=$!
held=""
attempt=0
while [ "$attempt" -lt 40 ]; do
  if grep -q '^run --rm ' "$HOME/stub.log" 2>/dev/null; then
    held=yes
    break
  fi
  attempt=$((attempt + 1))
  sleep 0.25
done
if [ -n "$held" ]; then
  pass "the first run reached the runtime and holds its leases"
else
  fail "the first run never reached the runtime, so the contention arm proves nothing"
fi

set +e
"$BIN" remote run "$HOME/remote-work/one" -- true >"$tmpdir/busy.out" 2>"$tmpdir/busy.err"
busy_rc=$?
set -e
if [ "$busy_rc" -eq 75 ]; then
  pass "a second run of the same path defers with exit 75"
else
  fail "a second run of the same path exited $busy_rc, want 75: $(cat "$tmpdir/busy.err")"
fi
if grep -q "another run of $HOME/remote-work/one is active" "$tmpdir/busy.out" "$tmpdir/busy.err"; then
  pass "the defer reason names the held path lease"
else
  fail "the defer reason does not name the held lease: $(cat "$tmpdir/busy.err")"
fi

# A different path is a different lease scope, so max_runs is what stops it.
set +e
"$BIN" remote run "$HOME/remote-work/two" -- true >"$tmpdir/maxruns.out" 2>"$tmpdir/maxruns.err"
maxruns_rc=$?
set -e
if [ "$maxruns_rc" -eq 75 ]; then
  pass "a run of another path defers at max_runs"
else
  fail "a run of another path exited $maxruns_rc, want 75 at max_runs: $(cat "$tmpdir/maxruns.err")"
fi
if grep -q "1 runs are already active on remotehost" "$tmpdir/maxruns.out" "$tmpdir/maxruns.err"; then
  pass "the defer reason names the host's max_runs"
else
  fail "the defer reason does not name max_runs: $(cat "$tmpdir/maxruns.err")"
fi

# The JSON record is the machine-readable half of the same defer.
set +e
"$BIN" remote run --json "$HOME/remote-work/two" -- true >"$tmpdir/json.out" 2>/dev/null
json_rc=$?
set -e
if [ "$json_rc" -eq 75 ] && grep -q '"outcome": "deferred"' "$tmpdir/json.out" && grep -q '"class": "remote"' "$tmpdir/json.out"; then
  pass "the defer record is JSON with outcome deferred and class remote"
else
  fail "the JSON defer record is wrong (exit $json_rc): $(cat "$tmpdir/json.out")"
fi

set +e
wait "$hold_pid"
hold_rc=$?
set -e
rm -f "$HOME/stub-hold"
if [ "$hold_rc" -eq 0 ]; then
  pass "the run that held the lease finished on its own"
else
  fail "the holding run exited $hold_rc: $(cat "$tmpdir/hold.err")"
fi

# ── host load ────────────────────────────────────────────────────────────────
# The ratio is small enough that any real load defers; the probe still reads
# the host, which is what the reason reports.
set +e
"$BIN" remote run --host loaded "$HOME/remote-work/two" -- true >"$tmpdir/load.out" 2>"$tmpdir/load.err"
load_rc=$?
set -e
if [ "$load_rc" -eq 75 ]; then
  pass "a host at its load limit defers with exit 75"
else
  fail "a host at its load limit exited $load_rc, want 75: $(cat "$tmpdir/load.err")"
fi
if grep -q "host load1 .* on .* CPUs is at or over" "$tmpdir/load.out" "$tmpdir/load.err"; then
  pass "the load defer reason carries the numbers the probe read"
else
  fail "the load defer reason carries no numbers: $(cat "$tmpdir/load.err")"
fi

# The bypass still reads the host and says what it found, so nothing is skipped
# silently (the same rule dot admit holds).
set +e
DOT_ADMISSION_IGNORE=remote-load "$BIN" remote run --host loaded "$HOME/remote-work/two" -- true \
  >"$tmpdir/bypass.out" 2>"$tmpdir/bypass.err"
bypass_rc=$?
set -e
if [ "$bypass_rc" -eq 0 ]; then
  pass "the load bypass lets the run through"
else
  fail "the load bypass exited $bypass_rc, want 0: $(cat "$tmpdir/bypass.err")"
fi
if grep -q "remote host load ignored (DOT_ADMISSION_IGNORE)" "$tmpdir/bypass.out" "$tmpdir/bypass.err"; then
  pass "the bypass names what it stepped past"
else
  fail "the bypass said nothing: $(cat "$tmpdir/bypass.err")"
fi

# ── interrupt cleanup ────────────────────────────────────────────────────────
printf '30\n' >"$HOME/stub-hold"
: >"$HOME/stub.log"
"$BIN" remote run "$HOME/remote-work/one" -- sleep 30 >"$tmpdir/int.out" 2>"$tmpdir/int.err" &
int_pid=$!
started=""
attempt=0
while [ "$attempt" -lt 40 ]; do
  if grep -q '^run --rm ' "$HOME/stub.log" 2>/dev/null; then
    started=yes
    break
  fi
  attempt=$((attempt + 1))
  sleep 0.25
done
if [ -n "$started" ]; then
  kill -INT "$int_pid"
  set +e
  wait "$int_pid"
  int_rc=$?
  set -e
  if [ "$int_rc" -eq 130 ]; then
    pass "an interrupted run reports 128+SIGINT"
  else
    fail "an interrupted run exited $int_rc, want 130"
  fi
  if grep -q 'rm -f' "$HOME/stub.log"; then
    pass "an interrupted run removes the container it started"
  else
    fail "an interrupted run left its container behind: $(cat "$HOME/stub.log")"
  fi
else
  kill "$int_pid" >/dev/null 2>&1 || true
  fail "the interrupted run never reached the runtime"
fi
rm -f "$HOME/stub-hold"

# An interrupt whose removal the runtime refuses has to say so and keep the
# claim: releasing it would invite another machine into a mirror that may still
# hold the container that would not go.
printf '30\n' >"$HOME/stub-hold"
touch "$HOME/stub-remove-fail"
: >"$HOME/stub.log"
"$BIN" remote run "$HOME/remote-work/one" -- sleep 30 >"$tmpdir/int2.out" 2>"$tmpdir/int2.err" &
int2_pid=$!
started=""
attempt=0
while [ "$attempt" -lt 40 ]; do
  if grep -q '^run --rm ' "$HOME/stub.log" 2>/dev/null; then
    started=yes
    break
  fi
  attempt=$((attempt + 1))
  sleep 0.25
done
if [ -n "$started" ]; then
  kill -INT "$int2_pid"
  set +e
  wait "$int2_pid"
  int2_rc=$?
  set -e
  if [ "$int2_rc" -eq 130 ] && grep -q 'could not remove the container' "$tmpdir/int2.err"; then
    pass "an interrupt whose removal fails says it could not remove the container"
  else
    fail "an interrupted run with a refused removal exited $int2_rc: $(cat "$tmpdir/int2.err")"
  fi
  if [ -d "$claim_dir" ]; then
    pass "the claim stays when an interrupted run cannot remove its container"
  else
    fail "the claim was released over a container the runtime would not remove"
  fi
else
  kill "$int2_pid" >/dev/null 2>&1 || true
  fail "the interrupt-with-refused-removal arm never reached the runtime"
fi
rm -f "$HOME/stub-hold" "$HOME/stub-remove-fail"
rm -rf "$claim_dir"

# ── status ───────────────────────────────────────────────────────────────────
if "$BIN" remote status >"$tmpdir/status.out" 2>"$tmpdir/status.err"; then
  pass "status exits 0"
  if grep -q "dot remote status" "$tmpdir/status.out" && grep -q "Mirrors" "$tmpdir/status.out"; then
    pass "status renders the host, the load and the mirror sizes"
  else
    fail "status rendered nothing useful: $(cat "$tmpdir/status.out")"
  fi
  if grep -q 'Mirrors:  *2 (' "$tmpdir/status.out"; then
    pass "status sized both mirrors recorded under one root"
  else
    fail "status did not size both mirrors: $(cat "$tmpdir/status.out")"
  fi
else
  fail "status exited non-zero: $(cat "$tmpdir/status.err")"
fi

# ── a path holding a space ───────────────────────────────────────────────────
# The far side of an rsync invocation is a shell command: a destination with a
# space in it is split there unless -s sends the paths over rsync's own channel.
# A local destination cannot show that, because only ssh joins its argv with
# spaces before the shell on the other end parses it.
mkdir -p "$HOME/remote-work/weird name"
printf 'a mirror whose path holds a space\n' >"$HOME/remote-work/weird name/note.txt"
git -C "$HOME/remote-work/weird name" -c init.defaultBranch=main init --quiet .
if "$BIN" remote run "$HOME/remote-work/weird name" -- true >"$tmpdir/space.out" 2>"$tmpdir/space.err"; then
  pass "a checkout whose path holds a space mirrors and runs"
else
  fail "a checkout whose path holds a space failed: $(cat "$tmpdir/space.err")"
fi
if [ "$(cat "$HOME/dot-remote/weird name/note.txt" 2>/dev/null)" = "a mirror whose path holds a space" ]; then
  pass "the spaced mirror holds the file byte for byte, so the ssh quoting held"
else
  fail "the spaced mirror does not hold note.txt: $(ls -A "$HOME/dot-remote/weird name" 2>/dev/null | tr '\n' ' ')"
fi

# ── the lease is visible where dot admit shows its own ───────────────────────
printf '6\n' >"$HOME/stub-hold"
"$BIN" remote run "$HOME/remote-work/one" -- sleep 6 >/dev/null 2>"$tmpdir/hold2.err" &
hold2_pid=$!
seen=""
attempt=0
while [ "$attempt" -lt 40 ]; do
  if "$BIN" admit status 2>/dev/null | grep -q 'remote-host:remotehost'; then
    seen=yes
    break
  fi
  attempt=$((attempt + 1))
  sleep 0.25
done
if [ -n "$seen" ]; then
  pass "dot admit status shows the remote lease"
else
  fail "dot admit status does not show a remote-host lease: $(dot admit status 2>&1 | head -20)"
fi
set +e
wait "$hold2_pid"
set -e
rm -f "$HOME/stub-hold"

# ── verify of a dirty tree ───────────────────────────────────────────────────
# The push and the bare repo are real (git over the ssh alias); only the
# container is the stub, so the assertions read the bare repo and the stub log.
vrepo="$HOME/remote-work/one"
git -C "$vrepo" -c user.name=t -c user.email=t@x add -A >/dev/null 2>&1 || true
git -C "$vrepo" -c user.name=t -c user.email=t@x commit -q -m base >/dev/null 2>&1 || true
printf 'uncommitted\n' >"$vrepo/dirty.txt"
index_before=$(sha1sum "$vrepo/.git/index" | cut -d' ' -f1)
head_before=$(git -C "$vrepo" rev-parse HEAD)
: >"$HOME/stub.log"
if "$BIN" remote verify --dirty "$vrepo" -- make check >"$tmpdir/verify.out" 2>"$tmpdir/verify.err"; then
  pass "verify --dirty of a checkout exited 0"
else
  fail "verify --dirty failed: $(cat "$tmpdir/verify.err")"
fi
bare=$(find "$HOME/dot-remote-git" -maxdepth 1 -name '*.git' -type d | head -1)
if [ -n "$bare" ] && [ "$(stat -c %a "$HOME/dot-remote-git")" = "700" ]; then
  pass "verify created an owner-only bare repo: $(basename "$bare")"
else
  fail "no owner-only bare repo under ~/dot-remote-git: $(ls -la "$HOME/dot-remote-git" 2>&1 | tr '\n' ';')"
fi
if [ -n "$bare" ] && [ -z "$(git -C "$bare" for-each-ref refs/verify)" ]; then
  pass "the verify ref is deleted after the job"
else
  fail "a verify ref is left: $(git -C "$bare" for-each-ref 2>&1 | tr '\n' ';')"
fi
if grep -q -- '/git/repo.git:ro' "$HOME/stub.log" && grep -q -- '--name e2e-rdev-verify-' "$HOME/stub.log" && grep -q 'make check' "$HOME/stub.log"; then
  pass "the runtime received the read-only bare mount, the verify container and the command"
else
  fail "verify container argv: $(grep '^run ' "$HOME/stub.log" | tail -1)"
fi
if [ "$(sha1sum "$vrepo/.git/index" | cut -d' ' -f1)" = "$index_before" ] && [ "$(git -C "$vrepo" rev-parse HEAD)" = "$head_before" ]; then
  pass "verify --dirty left the index and HEAD untouched"
else
  fail "verify --dirty changed the index or HEAD"
fi
rm -f "$vrepo/dirty.txt"

# ── src ──────────────────────────────────────────────────────────────────────
set +e
"$BIN" remote src nope -- ls >/dev/null 2>"$tmpdir/src.err"
src_code=$?
set -e
if [ "$src_code" -eq 66 ] && grep -q 'no clone nope' "$tmpdir/src.err"; then
  pass "src NAME with no clone exits 66 and says so"
else
  fail "src of a missing clone exited $src_code: $(cat "$tmpdir/src.err")"
fi
: >"$HOME/stub.log"
if "$BIN" remote src -- echo hi >/dev/null 2>"$tmpdir/src2.err" && [ -d "$HOME/dot-remote-src" ]; then
  pass "src without NAME runs in a created src_root"
else
  fail "src without NAME failed: $(cat "$tmpdir/src2.err")"
fi
if grep -q -- '--name e2e-rdev-src-root' "$HOME/stub.log" && ! grep -q -- '-v e2e-rdev-' "$HOME/stub.log"; then
  pass "src mounts no cache volume"
else
  fail "src argv: $(grep '^run ' "$HOME/stub.log" | tail -1)"
fi

# ── dot remote job ───────────────────────────────────────────────────────────
jobin="$tmpdir/job-in"
mkdir -p "$jobin"
printf 'payload\n' >"$jobin/a.txt"
if "$BIN" remote job check --host jobhost --profile media >/dev/null 2>"$tmpdir/jobcheck.err"; then
  pass "job check: the host can take a media job"
else
  fail "job check failed: $(cat "$tmpdir/jobcheck.err")"
fi
set +e
"$BIN" remote job check --host jobhost --profile full >/dev/null 2>&1
code=$?
set -e
if [ "$code" -eq 69 ]; then pass "job check exits 69 when the disk is too full"; else fail "job check on a full disk exited $code"; fi

# shellcheck disable=SC2016 # $THREADS expands inside the job, not here
if "$BIN" remote job run --host jobhost --profile media --name rt -o "$tmpdir/job-out" -i "$jobin/a.txt" \
  -- 'mkdir -p out && cp in/a.txt out/b.txt && echo "$THREADS" > out/threads' >/dev/null 2>"$tmpdir/job.err"; then
  pass "a job round trip exited 0"
else
  fail "job round trip failed: $(cat "$tmpdir/job.err")"
fi
if [ "$(cat "$tmpdir/job-out/b.txt" 2>/dev/null)" = "payload" ] && [ "$(cat "$tmpdir/job-out/threads" 2>/dev/null)" = "1" ]; then
  pass "out/ came back with the file and the templated env"
else
  fail "out/ did not come back: $(ls -A "$tmpdir/job-out" 2>&1 | tr '\n' ' ')"
fi
if [ ! -e "$HOME/e2e-jobs/rt" ]; then pass "a successful job's directory is deleted"; else fail "the job directory was kept after success"; fi

set +e
"$BIN" remote job run --host jobhost --profile media --name broken -o "$tmpdir/job-out2" -- 'exit 7' >/dev/null 2>"$tmpdir/job2.err"
code=$?
set -e
if [ "$code" -eq 7 ] && [ -d "$HOME/e2e-jobs/broken" ] && grep -q 'job kept at' "$tmpdir/job2.err"; then
  pass "a failed job exits with its status and keeps its directory"
else
  fail "failed job: exit $code, $(cat "$tmpdir/job2.err")"
fi

set +e
"$BIN" remote job run --host jobhost --profile full --remote-only --name nope -o "$tmpdir/job-out3" -- true >/dev/null 2>&1
code=$?
set -e
if [ "$code" -eq 69 ]; then pass "--remote-only exits 69 when the host cannot take the job"; else fail "--remote-only exited $code"; fi

if DOT_ADMISSION_IGNORE=pressure,uncovered "$BIN" remote job run --host jobhost --profile full --name fb -o "$tmpdir/job-out4" -i "$jobin/a.txt" \
  -- 'mkdir -p out && cp in/a.txt out/local.txt' >/dev/null 2>"$tmpdir/job4.err"; then
  if [ "$(cat "$tmpdir/job-out4/local.txt" 2>/dev/null)" = "payload" ] && grep -q 'running it locally' "$tmpdir/job4.err" && [ ! -e "$HOME/e2e-local-jobs/fb" ]; then
    pass "the job fell back to this machine, collected out/ and cleaned up"
  else
    fail "fallback output wrong: $(cat "$tmpdir/job4.err")"
  fi
else
  fail "fallback job failed: $(cat "$tmpdir/job4.err")"
fi

# A second job of a max_jobs=1 profile defers with 75 while the first holds it;
# then the held one is interrupted and its container removed.
printf '8\n' >"$HOME/stub-hold"
"$BIN" remote job run --host jobhost --profile media --name held -o "$tmpdir/job-out5" -- true >/dev/null 2>"$tmpdir/held.err" &
held_pid=$!
attempt=0
while [ "$attempt" -lt 40 ] && ! grep -qx 'e2e-job-job-media_held' "$HOME/stub-running" 2>/dev/null; do
  attempt=$((attempt + 1))
  sleep 0.25
done
set +e
"$BIN" remote job run --host jobhost --profile media --name second -o "$tmpdir/job-out6" -- true >/dev/null 2>&1
code=$?
set -e
if [ "$code" -eq 75 ]; then pass "a second job of the profile defers with 75"; else fail "second job exited $code"; fi
# TERM, because a background job of a non-interactive shell starts with SIGINT ignored.
kill -TERM "$held_pid" 2>/dev/null || true
set +e
wait "$held_pid"
code=$?
set -e
rm -f "$HOME/stub-hold"
if [ "$code" -eq 143 ] && grep -q '^rm -f e2e-job-job-media_held' "$HOME/stub.log"; then
  pass "an interrupted job exits 128+signal and removes its container"
else
  fail "interrupt: exit $code, $(cat "$tmpdir/held.err")"
fi

# ── prune and -p ─────────────────────────────────────────────────────────────
mkdir -p "$HOME/remote-work/doomed"
printf 'x\n' >"$HOME/remote-work/doomed/f.txt"
git -C "$HOME/remote-work/doomed" -c init.defaultBranch=main init --quiet .
: >"$HOME/stub.log"
if "$BIN" remote run -p 18080 "$HOME/remote-work/doomed" -- true >/dev/null 2>"$tmpdir/port.err"; then
  if grep -q -- '-p 127.0.0.1:18080:18080' "$HOME/stub.log"; then
    pass "-p publishes the port on the host loopback"
  else
    fail "-p argv: $(grep '^run ' "$HOME/stub.log" | tail -1)"
  fi
else
  fail "run -p failed: $(cat "$tmpdir/port.err")"
fi
rm -rf "$HOME/remote-work/doomed"
"$BIN" remote prune --json >"$tmpdir/prune.json" 2>"$tmpdir/prune.err" || true
if grep -q '"name": "doomed"' "$tmpdir/prune.json" && [ -d "$HOME/dot-remote/doomed" ]; then
  pass "prune lists the mirror of a deleted checkout and removes nothing"
else
  fail "prune listing: $(cat "$tmpdir/prune.json" "$tmpdir/prune.err")"
fi
if grep -q '"name": "one"' "$tmpdir/prune.json"; then
  fail "prune listed a mirror whose checkout still exists"
else
  pass "prune leaves the mirror of a live checkout alone"
fi
if "$BIN" remote prune --yes >/dev/null 2>"$tmpdir/prune2.err" && [ ! -e "$HOME/dot-remote/doomed" ] && [ -d "$HOME/dot-remote/one" ]; then
  pass "prune --yes removes only the orphaned mirror"
else
  fail "prune --yes: $(cat "$tmpdir/prune2.err")"
fi

# D-10: `report` alone returns success on zero assertions, so the verdict
# conjoins a positive pass count on its own line.
report
[ "$PASS" -gt 0 ]
