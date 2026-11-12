package remote

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// rsyncMinimum is the rsync version the two-pass sync needs on both ends:
// --mkpath arrived in 3.2.3.
const rsyncMinimum = "3.2.3"

// gitListArgs prints git's view of the tree: tracked files plus untracked ones
// that no ignore rule covers. Build products and secrets never appear in it.
func gitListArgs() []string {
	return []string{"ls-files", "-z", "--cached", "--others", "--exclude-standard"}
}

// Step is one rsync invocation and the stdin it is fed.
type Step struct {
	Argv  []string
	Stdin []byte
	// Tolerate is the one non-zero status this step's own meaning makes benign,
	// or 0. The delete pass names paths the checkout no longer has, and rsync
	// exits 24 ("some files vanished before they could be transferred") for a
	// path whose parent directory is gone from the source: that is the answer it
	// was given, and the deletion still happened. Every other status is a
	// failure, and so is 24 anywhere else.
	Tolerate int
}

// vanishedStatus is rsync's exit status for "some files vanished while the
// transfer was running": a warning about the source, not a failed transfer.
const vanishedStatus = 24

// SyncInput is what one sync of a checkout was decided on.
type SyncInput struct {
	// Excludes is the operator's exclude list, sent as rsync filters.
	Excludes []string
	// Listing is git's view of the checkout, NUL-separated. nil means src is not
	// a checkout, which gets one plain mirroring pass with the exclude list.
	Listing []byte
	// Deletes is what to remove from the mirror, NUL-separated: paths this
	// machine sent last time, or that the mirror is holding, that the checkout
	// neither lists nor has. Empty means the delete pass does not run at all.
	Deletes []byte
}

// SyncPlan builds the transfer of src into dst.
//
// A git checkout gets two passes, deletes first. Naming the paths that left the
// checkout before sending the new tree is what lets one of them be a directory
// that became a file or a symlink: the ship pass cannot make way for a
// non-directory over a non-empty directory, and it would fail before the delete
// pass had run, leaving the mirror on the old tree with every later run failing
// the same way.
//
// The delete pass removes the paths in SyncInput.Deletes, and only those:
// --delete-missing-args removes a named path the source no longer has and
// leaves everything else, so a build output that was never sent is not a
// candidate at all. That is the whole point of naming the set - a filter file
// cannot say what git's ignore rules say: rsync reads .gitignore's patterns as
// plain excludes, so with `*.lock` and `!Cargo.lock` in one file a Cargo.lock
// that git lists, and that this machine sent, is protected from deletion by the
// very pattern the negation was meant to override, and the mirror keeps a file
// the checkout no longer has.
//
// The ship pass sends exactly the files git lists, so a file deleted locally is
// simply absent, and carries --force: that is the only way rsync replaces a
// directory holding something the delete pass did not name - a build output the
// host made inside a directory the checkout turned into a file - and it acts on
// nothing else, because a path it was not given is not considered for deletion.
//
// A path that is not a git checkout (Listing is nil) gets one plain mirroring
// pass with the same exclude list and --delete, which replaces a directory by
// itself: everything under it is an extra and goes with the directory.
func SyncPlan(rsyncPath, src, dst string, in SyncInput) []Step {
	// -s (--secluded-args) sends the paths over rsync's own channel instead of
	// interpolating them into the remote shell's command line. Without it a
	// destination holding a space, ';' or '$(...)' is parsed by the shell on the
	// far side, because ssh joins its arguments with spaces.
	base := append([]string{"-a", "-s", "--mkpath"}, ExcludeArgs(in.Excludes)...)
	if in.Listing == nil {
		argv := append(append([]string{}, base...), "--delete", src+"/", dst+"/")
		return []Step{{Argv: append([]string{rsyncPath}, argv...)}}
	}
	var steps []Step
	if len(in.Deletes) > 0 {
		drop := append(append([]string{}, base...),
			"--files-from=-", "--from0", "--delete-missing-args", src+"/", dst+"/")
		steps = append(steps, Step{Argv: append([]string{rsyncPath}, drop...), Stdin: in.Deletes, Tolerate: vanishedStatus})
	}
	ship := append(append([]string{}, base...),
		"--files-from=-", "--from0", "--ignore-missing-args", "--force", src+"/", dst+"/")
	return append(steps, Step{Argv: append([]string{rsyncPath}, ship...), Stdin: in.Listing})
}

// SSHArgv is one remote command over ssh. BatchMode keeps a password or host
// key question from hanging a run: the host has to be reachable
// non-interactively already.
func SSHArgv(dest, remoteCommand string) []string {
	return []string{"ssh", "-o", "BatchMode=yes", dest, remoteCommand}
}

// ContainerArgv is the runtime's argv for a run, starting at "run".
//
// The mirror mount is deliberately absent: its source is `"$HOME"/<mirror>`,
// which has to expand on the host rather than here, so RunCommand splices it in
// after --rm. Everything else is quoted here.
func ContainerArgv(h Host, name, rel string, command []string) []string {
	return ContainerArgvWith(h, name, rel, command, RunExtras{})
}

// RunExtras are a run's optional container settings (#421 phase C).
type RunExtras struct {
	// Ports are published on the host loopback only, 127.0.0.1:PORT:PORT.
	Ports []int
	// Cgroup starts the container as root with CAP_SYS_ADMIN and a private
	// cgroup namespace, delegates a writable cgroup v2 subtree, then drops to the
	// image's user with no capabilities before CMD runs.
	Cgroup bool
}

// ContainerArgvWith is ContainerArgv with the run's extras.
func ContainerArgvWith(h Host, name, rel string, command []string, extra RunExtras) []string {
	argv := []string{"run", "--rm", "--init", "-i", "--name", name}
	if h.CPUs > 0 {
		argv = append(argv, "--cpus", strconv.Itoa(h.CPUs))
	}
	argv = append(argv, "--memory", h.Memory, "-w", h.WorkDir(rel))
	for _, cache := range h.Caches {
		argv = append(argv, "-v", h.Volume(cache.Name)+":"+cache.Target)
	}
	for _, pair := range h.EnvPairs() {
		argv = append(argv, "-e", pair.Key+"="+pair.Value)
	}
	for _, port := range extra.Ports {
		argv = append(argv, "-p", fmt.Sprintf("127.0.0.1:%d:%d", port, port))
	}
	if extra.Cgroup {
		argv = append(argv, "--user", "0", "--cap-add", "SYS_ADMIN", "--security-opt", "apparmor=unconfined", "--cgroupns", "private")
		return append(argv, h.Image(), "bash", "-c", CgroupScript(command))
	}
	argv = append(argv, h.Image(), "bash", "-c", CommandScript(command))
	return argv
}

// cgroupSetup runs as root inside the container: it moves every process into
// an init leaf, enables the memory controller, creates the delegated subtree
// owned by the image's user, and puts this shell in a leaf of it. Any failure
// says what the host must allow and stops before CMD runs.
const cgroupSetup = `set -e; c=/sys/fs/cgroup; ` +
	`if [ -z "$DOT_REMOTE_USER" ] || ! id "$DOT_REMOTE_USER" >/dev/null 2>&1; then echo "dot remote: --cgroup needs the image to set a USER that has an /etc/passwd entry (got '$DOT_REMOTE_USER'); add one and rerun dot remote setup" >&2; exit 1; fi; ` +
	`trap 'echo "dot remote: --cgroup setup failed (line $LINENO): the host must use cgroup v2 and the runtime must allow CAP_SYS_ADMIN with AppArmor unconfined; rerun without --cgroup if the command needs no delegated cgroup" >&2' ERR; ` +
	`mount -o remount,rw "$c"; mkdir "$c/init"; ` +
	`for pid in $(cat "$c/cgroup.procs"); do echo "$pid" > "$c/init/cgroup.procs" 2>/dev/null || true; done; ` +
	`echo +memory > "$c/cgroup.subtree_control"; mkdir -p "$c/delegated/harness"; ` +
	`echo +memory > "$c/delegated/cgroup.subtree_control"; chown -R "$DOT_REMOTE_USER" "$c/delegated"; ` +
	`echo $$ > "$c/delegated/harness/cgroup.procs"; trap - ERR; set +e; export DOT_REMOTE_CGROUP="$c/delegated"; `

// CgroupScript is the root-side script of a --cgroup run: the delegation, then
// CMD as the image's own user (read on the host into DOT_REMOTE_USER) with
// every capability dropped from the inheritable and bounding sets.
func CgroupScript(command []string) string {
	return cgroupSetup + `home=$(getent passwd "$DOT_REMOTE_USER" | cut -d: -f6); ` +
		`exec setpriv --reuid="$DOT_REMOTE_USER" --regid="$(id -g "$DOT_REMOTE_USER")" --init-groups --inh-caps=-all --bounding-set=-all ` +
		`env HOME="${home:-/tmp}" PATH="$PATH" DOT_REMOTE_CGROUP="$DOT_REMOTE_CGROUP" bash -c ` + ShellQuote(CommandScript(command))
}

// RunSSHArgv is the ssh argv of one run. With ports, each is forwarded from
// local 127.0.0.1:PORT to the host's 127.0.0.1:PORT over this same connection
// for the life of the run; a port already taken locally fails the run rather
// than running without its forward.
func RunSSHArgv(h Host, ports []int, remoteCommand string) []string {
	argv := []string{"ssh", "-o", "BatchMode=yes"}
	if len(ports) > 0 {
		argv = append(argv, "-o", "ExitOnForwardFailure=yes")
		for _, port := range ports {
			argv = append(argv, "-L", fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", port, port))
		}
	}
	return append(argv, h.SSH, remoteCommand)
}

// RunCommand is the whole remote shell command a run sends: the runtime, the
// $HOME-relative mirror mount and the container argv.
func RunCommand(h Host, name, rel string, command []string) string {
	return RunCommandWith(h, name, rel, command, RunExtras{})
}

// RunCommandWith is RunCommand with the run's extras. A --cgroup run starts as
// root, so the image's own user is read on the host first and handed in as
// DOT_REMOTE_USER for the drop.
func RunCommandWith(h Host, name, rel string, command []string, extra RunExtras) string {
	argv := ContainerArgvWith(h, name, rel, command, extra)
	mount := `-v "$HOME"/` + ShellQuote(h.MirrorDir(rel)) + ":" + ShellQuote(h.WorkDir(rel))
	if extra.Cgroup {
		user := `DOT_REMOTE_USER=$(` + h.Runtime + ` image inspect -f '{{.Config.User}}' ` + ShellQuote(h.Image()) + `) && DOT_REMOTE_USER=${DOT_REMOTE_USER%%:*} && `
		return user + h.Runtime + ` run --rm -e DOT_REMOTE_USER="$DOT_REMOTE_USER" ` + mount + " " + ShellJoin(argv[2:])
	}
	return h.Runtime + " run --rm " + mount + " " + ShellJoin(argv[2:])
}

// CommandScript renders CMD the way a container receives it: one argument is
// already a shell script, several are an argv to quote and join.
func CommandScript(command []string) string {
	if len(command) == 1 {
		return command[0]
	}
	return ShellJoin(command)
}

// ShellQuote wraps one word in single quotes, which is the only form sh cannot
// misread. A quote inside the word is closed, escaped and reopened.
func ShellQuote(word string) string {
	if word == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(word, "'", `'\''`) + "'"
}

// safeWord matches a word a POSIX shell passes through unchanged. It is the
// set shlex.quote keeps bare, so a rendered command line reads like the one a
// person would type.
var safeWord = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// ShellJoin quotes and joins an argv into one POSIX shell command line, leaving
// words that need no quoting bare.
func ShellJoin(argv []string) string {
	quoted := make([]string, len(argv))
	for i, word := range argv {
		if safeWord.MatchString(word) {
			quoted[i] = word
			continue
		}
		quoted[i] = ShellQuote(word)
	}
	return strings.Join(quoted, " ")
}

// CheckRsyncVersion reports whether a rsync version banner is new enough for
// the two-pass sync, returning the version it read.
func CheckRsyncVersion(banner string) (string, error) {
	if strings.Contains(strings.ToLower(banner), "openrsync") {
		// openrsync reports a 29 protocol version and none of the 3.x flags.
		return "", fmt.Errorf("openrsync cannot serve --files-from --from0: install rsync %s or newer (brew install rsync)", rsyncMinimum)
	}
	version := parseRsyncVersion(banner)
	if version == "" {
		return "", fmt.Errorf("cannot read a version from %q", firstLine(banner))
	}
	if compareVersions(version, rsyncMinimum) < 0 {
		return version, fmt.Errorf("rsync %s is too old: the two-pass sync needs %s or newer (--mkpath)", version, rsyncMinimum)
	}
	return version, nil
}

func parseRsyncVersion(banner string) string {
	for _, field := range strings.Fields(firstLine(banner)) {
		if isVersion(field) {
			return field
		}
	}
	return ""
}

// isVersion reports whether field looks like a dotted version number.
func isVersion(field string) bool {
	parts := strings.Split(field, ".")
	if len(parts) < 2 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}

// compareVersions compares dotted versions numerically, padding the shorter
// one. A non-numeric part sorts as 0, which is what a "3.2.3pre1" banner gets.
func compareVersions(a, b string) int {
	left, right := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(left) || i < len(right); i++ {
		var l, r int
		if i < len(left) {
			l, _ = strconv.Atoi(left[i])
		}
		if i < len(right) {
			r, _ = strconv.Atoi(right[i])
		}
		if l != r {
			if l < r {
				return -1
			}
			return 1
		}
	}
	return 0
}

// firstLine returns the first line of a command's output, trimmed.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}
