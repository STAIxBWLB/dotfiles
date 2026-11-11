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
if rsync --version | head -1 | grep -q 'rsync  version 3\.[2-9]'; then
  pass "the image's rsync is 3.2 or newer: $(rsync --version | head -1)"
else
  ABORT "fixture: the image's rsync is too old for --mkpath: $(rsync --version | head -1)"
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
    echo "stub-container-ran"
    # --rm: nothing is left once the command returns.
    if [ -n "$name" ]; then forget "$name"; fi
    ;;
  rm)
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

# Port before ListenAddress: sshd applies a bare ListenAddress against the Port
# directives seen so far.
#
# UsePAM=no          jammy's /etc/pam.d/sshd has pam_loginuid.so as `required`
#                    and it fails in a container. Throwaway daemon in an
#                    ephemeral container; do not copy this into a real one.
# PasswordAuthentication=no  public key only.
# PermitRootLogin=no this user is not root.
# ListenAddress=127.0.0.1  the socket must not leave the container.
# SetEnv=PATH        a non-login, non-interactive shell gets sshd's own PATH,
#                    which does not carry the stub's directory.
sudo "$SSHD_BIN" \
  -o "Port=$SSH_PORT" \
  -o UsePAM=no \
  -o PasswordAuthentication=no \
  -o PermitRootLogin=no \
  -o ListenAddress=127.0.0.1 \
  -o "PidFile=$PIDFILE" \
  -o "SetEnv=PATH=$HOME/bin:/usr/local/bin:/usr/bin:/bin"

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
[ -n "$sshd_ready" ] || ABORT "fixture: sshd never accepted a connection on port $SSH_PORT"

ssh-keygen -t ed25519 -N '' -C ssh-remote-scenario -f "$tmpdir/id_ed25519" >/dev/null
install -d -m 700 "$HOME/.ssh"
cat "$tmpdir/id_ed25519.pub" >>"$HOME/.ssh/authorized_keys"
chmod 600 "$HOME/.ssh/authorized_keys"

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
remote_user=$(ssh remotehost 'id -un' 2>"$SSH_ERR" || true)
if [ "$remote_user" = "$(id -un)" ]; then
  pass "ssh to the alias works and answers as $(id -un)"
else
  ABORT "fixture: ssh remotehost failed: got '$remote_user' [ssh: $(tr '\n' ' ' <"$SSH_ERR")]"
fi
remote_docker=$(ssh remotehost 'command -v docker' 2>"$SSH_ERR" || true)
if [ "$remote_docker" = "$STUB" ]; then
  pass "a non-interactive ssh session resolves the stub runtime at $STUB"
else
  ABORT "fixture: the ssh session's PATH does not reach the stub: got '$remote_docker'"
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
EOF

# ── setup ────────────────────────────────────────────────────────────────────
setup_err="$tmpdir/setup.err"
if "$BIN" remote setup >"$tmpdir/setup.out" 2>"$setup_err"; then
  if grep -q "pull 'example.invalid/e2e:1'" "$HOME/stub.log"; then
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

# D-10: `report` alone returns success on zero assertions, so the verdict
# conjoins a positive pass count on its own line.
report
[ "$PASS" -gt 0 ]
