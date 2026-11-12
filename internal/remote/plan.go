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

// Step is one rsync invocation and the stdin it is fed. Pass one ships the
// listed files; pass two only deletes.
type Step struct {
	Argv  []string
	Stdin []byte
}

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
// A git checkout gets two passes. The first sends exactly the files git lists,
// so a file deleted locally is simply absent.
//
// The second deletes the paths in SyncInput.Deletes, and only those:
// --delete-missing-args removes a named path the source no longer has and
// leaves everything else, so a build output that was never sent is not a
// candidate at all. That is the whole point of naming the set - a filter file
// cannot say what git's ignore rules say: rsync reads .gitignore's patterns as
// plain excludes, so with `*.lock` and `!Cargo.lock` in one file a Cargo.lock
// that git lists, and that this machine sent, is protected from deletion by the
// very pattern the negation was meant to override, and the mirror keeps a file
// the checkout no longer has.
//
// A path that is not a git checkout (Listing is nil) gets one plain mirroring
// pass with the same exclude list and --delete.
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
	ship := append(append([]string{}, base...),
		"--files-from=-", "--from0", "--ignore-missing-args", src+"/", dst+"/")
	steps := []Step{{Argv: append([]string{rsyncPath}, ship...), Stdin: in.Listing}}
	if len(in.Deletes) > 0 {
		drop := append(append([]string{}, base...),
			"--files-from=-", "--from0", "--delete-missing-args", src+"/", dst+"/")
		steps = append(steps, Step{Argv: append([]string{rsyncPath}, drop...), Stdin: in.Deletes})
	}
	return steps
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
	argv = append(argv, h.Image(), "bash", "-c", CommandScript(command))
	return argv
}

// RunCommand is the whole remote shell command a run sends: the runtime, the
// $HOME-relative mirror mount and the container argv.
func RunCommand(h Host, name, rel string, command []string) string {
	argv := ContainerArgv(h, name, rel, command)
	if len(argv) < 2 {
		return h.Runtime + " " + ShellJoin(argv)
	}
	mount := `-v "$HOME"/` + ShellQuote(h.MirrorDir(rel)) + ":" + ShellQuote(h.WorkDir(rel))
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
