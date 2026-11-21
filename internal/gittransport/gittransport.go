// Package gittransport picks the protocol git uses for GitHub remotes. It
// probes SSH and HTTPS and writes the chosen insteadOf block to a mode file
// that the managed git config includes (#467). Git has no per-request
// fallback, so the switch has to live in config.
package gittransport

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// FileName is the mode file, included from ~/.config/git/config.
const FileName = "github-transport.inc"

type Mode string

const (
	ModeSSH   Mode = "ssh"
	ModeHTTPS Mode = "https"
)

// The whole mode lives in one file: git takes the longest insteadOf prefix
// and, on a tie, the first one read, so an override layered in another file
// would lose to the rule it means to replace.
const (
	sshBlock = `# Managed by dot (dot git transport). Do not edit.
[url "git@github.com:"]
	insteadOf = https://github.com/
`
	httpsBlock = `# Managed by dot (dot git transport). Do not edit.
[url "https://github.com/"]
	insteadOf = git@github.com:
	insteadOf = ssh://git@github.com/
`
)

func Path(home string) string { return filepath.Join(home, ".config", "git", FileName) }

func Block(m Mode) string {
	if m == ModeHTTPS {
		return httpsBlock
	}
	return sshBlock
}

// Current reads the mode file: "" when it is missing or holds content dot did
// not write.
func Current(home string) (Mode, error) {
	b, err := os.ReadFile(Path(home))
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	switch string(b) {
	case sshBlock:
		return ModeSSH, nil
	case httpsBlock:
		return ModeHTTPS, nil
	}
	return "", nil
}

// Write replaces the mode file atomically, only when its content changes.
func Write(home string, m Mode) (bool, error) {
	path := Path(home)
	want := []byte(Block(m))
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, want) {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".github-transport-*")
	if err != nil {
		return false, err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err := f.Write(want); err != nil {
		_ = f.Close()
		return false, err
	}
	if err := f.Chmod(0o644); err != nil {
		_ = f.Close()
		return false, err
	}
	if err := f.Close(); err != nil {
		return false, err
	}
	return true, os.Rename(f.Name(), path)
}

// Result is one transport's probe outcome.
type Result struct {
	Usable bool   `json:"usable"`
	Detail string `json:"detail"`
}

// ClassifySSH reads `ssh -T git@github.com` output. Only a successful
// authentication makes SSH usable: a reset, timeout, DNS failure or rejected
// key all leave git unable to fetch over SSH.
func ClassifySSH(output string) Result {
	line := lastLine(output)
	if strings.Contains(output, "successfully authenticated") {
		return Result{Usable: true, Detail: "authenticated"}
	}
	if line == "" {
		line = "no output"
	}
	return Result{Detail: line}
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// Decide returns the mode to write and whether it differs from current.
// With both transports unusable the current mode is kept: switching would
// trade one broken path for another.
func Decide(current Mode, ssh, https Result) (Mode, bool) {
	switch {
	case ssh.Usable:
		return ModeSSH, current != ModeSSH
	case https.Usable:
		return ModeHTTPS, current != ModeHTTPS
	case current == "":
		return ModeSSH, true
	}
	return current, false
}

// Prober runs the two probes. Its funcs are replaceable in tests.
type Prober struct {
	SSH   func(ctx context.Context) string
	HTTPS func(ctx context.Context) Result
}

func NewProber() *Prober {
	return &Prober{SSH: probeSSH, HTTPS: probeHTTPS}
}

func probeSSH(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, "ssh", "-T", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "git@github.com").CombinedOutput()
	if ctx.Err() != nil {
		return "probe timed out"
	}
	return string(out)
}

// probeHTTPS needs github.com reachable and a credential that git can fill
// without a prompt (a helper such as gh auth git-credential). The password is
// never kept or printed.
func probeHTTPS(ctx context.Context) Result {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, "https://github.com/", nil)
	if err != nil {
		return Result{Detail: err.Error()}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Result{Detail: "github.com unreachable: " + err.Error()}
	}
	_ = resp.Body.Close()

	cmd := exec.CommandContext(ctx, "git", "credential", "fill")
	cmd.Stdin = strings.NewReader("protocol=https\nhost=github.com\n\n")
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=", "SSH_ASKPASS=")
	out, err := cmd.Output()
	if err != nil || !hasPassword(string(out)) {
		return Result{Detail: "no non-interactive credential for https://github.com"}
	}
	return Result{Usable: true, Detail: "reachable, credential available"}
}

func hasPassword(fill string) bool {
	for _, line := range strings.Split(fill, "\n") {
		if v, ok := strings.CutPrefix(line, "password="); ok && v != "" {
			return true
		}
	}
	return false
}

// Report is the outcome of one Run.
type Report struct {
	Before  Mode   `json:"before"`
	After   Mode   `json:"after"`
	Changed bool   `json:"changed"`
	SSH     Result `json:"ssh"`
	HTTPS   Result `json:"https"`
	File    string `json:"file"`
	// Included is false when ~/.config/git/config does not include the
	// mode file yet (dot apply has not run since the upgrade), so the mode
	// has no effect.
	Included bool `json:"included"`
}

// Included reports whether the managed git config includes the mode file.
func Included(home string) bool {
	b, err := os.ReadFile(filepath.Join(home, ".config", "git", "config"))
	return err == nil && bytes.Contains(b, []byte(FileName))
}

// Run probes both transports and, unless check is set, writes the decided mode.
func (p *Prober) Run(ctx context.Context, home string, check bool) (Report, error) {
	before, err := Current(home)
	if err != nil {
		return Report{}, err
	}
	ssh := ClassifySSH(p.SSH(ctx))
	https := Result{Detail: "not probed (ssh usable)"}
	if !ssh.Usable {
		https = p.HTTPS(ctx)
	}
	after, change := Decide(before, ssh, https)
	r := Report{Before: before, After: after, SSH: ssh, HTTPS: https, File: Path(home), Included: Included(home)}
	if check || !change {
		return r, nil
	}
	if r.Changed, err = Write(home, after); err != nil {
		return r, fmt.Errorf("writing %s: %w", r.File, err)
	}
	return r, nil
}
