package syncer

import (
	"context"
	"fmt"
	"io"
	osexec "os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

// CheckRsync checks if rsync is installed and returns its version.
// RemoteRsyncPath resolves an rsync on the peer that can actually serve a
// modern client, and returns the value for --rsync-path (empty means "the
// default is fine").
//
// ponytail: known ceiling. See docs/CEILINGS.md (preview protocol limit).
// macOS 26 replaced rsync with openrsync, which reports "protocol version 29 /
// rsync 2.6.9 compatible" and cannot receive -aHAX from a 3.x client: the real
// transfer dies with "error in rsync protocol data stream (code 12)". A
// --dry-run does NOT surface this, because it never ships file data - so this
// has to be probed before the transfer, not discovered during it.
//
// Two traps this navigates. A non-interactive ssh shell does not source the
// Homebrew shellenv, so bare `rsync` resolves to /usr/bin/rsync even on a
// machine that has a proper 3.x installed two directories away. And the probe
// must therefore try absolute paths rather than trust PATH.
func RemoteRsyncPath(ctx context.Context, runner *exec.Runner, host string) (string, error) {
	candidates := []string{"rsync", "/opt/homebrew/bin/rsync", "/usr/local/bin/rsync"}
	var lastVer string
	for _, cand := range candidates {
		res, err := runner.Run(ctx, "ssh",
			"-o", "BatchMode=yes", "-o", "ConnectTimeout=5", host,
			cand+" --version 2>&1 | head -2")
		if err != nil {
			continue
		}
		ver := strings.TrimSpace(res.Stdout)
		if ver == "" {
			continue
		}
		lastVer = ver
		if remoteRsyncUsable(ver) {
			if cand == "rsync" {
				return "", nil // default is fine
			}
			return cand, nil
		}
	}
	if lastVer == "" {
		return "", fmt.Errorf("no rsync found on %s", host)
	}
	return "", fmt.Errorf("peer %s only offers openrsync/2.x (%q), which cannot receive -aHAX; install rsync 3.x there (brew install rsync)",
		host, firstLine(lastVer))
}

// localRsyncCandidates are probed in order by LocalRsyncPath. Tests swap them
// for fixtures; production never changes them.
var localRsyncCandidates = []string{"rsync", "/opt/homebrew/bin/rsync", "/usr/local/bin/rsync"}

// LocalRsyncPath resolves the local rsync client a peer run must use and
// returns its absolute path and version banner.
//
// The trap RemoteRsyncPath navigates exists on this side too: a non-login
// shell (an ssh command, `zsh -l -s` fed by a pipe, a plain script) puts
// /usr/bin ahead of Homebrew, so bare `rsync` is openrsync on macOS 26 even
// with rsync 3.x installed. openrsync escapes non-ASCII names in --out-format
// as \#ooo, and the inventory then rejected valid NFD names as unnormalized
// (#175). Every local invocation of a peer run uses the path returned here.
func LocalRsyncPath(ctx context.Context, runner *exec.Runner) (string, string, error) {
	return localRsyncPath(ctx, runner, localRsyncCandidates)
}

func localRsyncPath(ctx context.Context, runner *exec.Runner, candidates []string) (string, string, error) {
	var rejected []string
	seen := map[string]bool{}
	for _, cand := range candidates {
		path := cand
		if !filepath.IsAbs(path) {
			found, err := osexec.LookPath(cand)
			if err != nil {
				continue
			}
			if path, err = filepath.Abs(found); err != nil {
				continue
			}
		}
		if seen[path] {
			continue
		}
		seen[path] = true
		res, err := runner.RunQuery(ctx, path, "--version")
		if err != nil {
			continue
		}
		ver := strings.TrimSpace(res.Stdout)
		if remoteRsyncUsable(ver) {
			return path, firstLine(ver), nil
		}
		rejected = append(rejected, fmt.Sprintf("%s (%s)", path, strings.ReplaceAll(ver, "\n", ", ")))
	}
	if len(rejected) == 0 {
		return "", "", fmt.Errorf("no local rsync found (tried %s); install rsync 3.x (brew install rsync)", strings.Join(candidates, ", "))
	}
	return "", "", fmt.Errorf("local rsync is openrsync or 2.x: %s; sync needs rsync 3.x (brew install rsync)", strings.Join(rejected, "; "))
}

// ResolvePushRsync pins a modern client for a local mirror push. Without
// delete propagation an older client remains usable; with backups, deletes
// require rsync 3.x before planning or normalization can change anything.
func ResolvePushRsync(ctx context.Context, runner *exec.Runner, cfg *Config) error {
	if cfg.Target.IsSSH() || !cfg.Propagation.Delete {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("push interrupted: %w", err)
	}
	candidates := localRsyncCandidates
	if cfg.RsyncPath != "" {
		candidates = append([]string{cfg.RsyncPath}, candidates...)
	}
	path, _, err := localRsyncPath(ctx, runner, candidates)
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("push interrupted: %w", err)
	}
	if err != nil {
		return fmt.Errorf("rsync 3.x is needed to delete with a backup; brew install rsync: %w", err)
	}
	cfg.RsyncPath = path
	return nil
}

// rsyncBin is the local rsync client for this run: the binary a peer run
// resolved and verified, else the PATH lookup made when the config was
// resolved, else bare "rsync".
func (c *Config) rsyncBin() string {
	if c != nil && c.RsyncPath != "" {
		return c.RsyncPath
	}
	return "rsync"
}

// resolvePeerRsync pins both rsync ends of a peer run: the local client
// (absolute path, 3.x) and the remote --rsync-path. Both fail before any
// transfer.
func resolvePeerRsync(ctx context.Context, probe *exec.Runner, cfg *Config) error {
	local, _, err := LocalRsyncPath(ctx, probe)
	if err != nil {
		return err
	}
	cfg.RsyncPath = local
	rp, err := RemoteRsyncPath(ctx, probe, cfg.Target.Host)
	if err != nil {
		return err
	}
	cfg.RemoteRsyncPath = rp
	return nil
}

func remoteRsyncUsable(version string) bool {
	v := strings.ToLower(version)
	if strings.Contains(v, "openrsync") {
		return false
	}
	// "rsync  version 2.6.9" and the "2.6.9 compatible" banner both disqualify.
	if strings.Contains(v, "version 2.") {
		return false
	}
	return strings.Contains(v, "version 3.") || strings.Contains(v, "version 4.")
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func CheckRsync(runner *exec.Runner) (string, bool) {
	if !runner.CommandExists("rsync") {
		return "", false
	}
	result, err := runner.Run(context.Background(), "rsync", "--version")
	if err != nil {
		return "", false
	}
	// First line: "rsync  version 3.3.0  protocol version 31"
	lines := strings.Split(strings.TrimSpace(result.Stdout), "\n")
	if len(lines) > 0 {
		return strings.TrimSpace(lines[0]), true
	}
	return "unknown", true
}

// InstallRsync installs rsync via brew or apt. Progress lines go to out;
// a nil out means process stdout.
func InstallRsync(ctx context.Context, runner *exec.Runner, out io.Writer) error {
	out = outOrStdout(out)
	brew := exec.NewBrew(runner)
	if brew.IsAvailable() {
		fmt.Fprintln(out, "Installing rsync via Homebrew...")
		return brew.Install(ctx, []string{"rsync"})
	}

	if runtime.GOOS == "linux" {
		fmt.Fprintln(out, "Installing rsync via apt...")
		_, err := runner.Run(ctx, "sudo", "apt-get", "install", "-y", "rsync")
		return err
	}

	return fmt.Errorf("cannot auto-install rsync: install Homebrew first or use your package manager")
}

// CheckSSH verifies SSH connectivity to a remote host (5s timeout).
func CheckSSH(ctx context.Context, runner *exec.Runner, host string) error {
	timeoutCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err := runner.Run(timeoutCtx, "ssh",
		"-o", "ConnectTimeout=5",
		"-o", "BatchMode=yes",
		// ponytail: known ceiling. See docs/CEILINGS.md (first-contact trust).
		"-o", "StrictHostKeyChecking=accept-new",
		host, "echo ok")
	if err != nil {
		if timeoutCtx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("SSH to %s timed out (5s) — check VPN/network", host)
		}
		return fmt.Errorf("SSH to %s failed: %w", host, err)
	}
	return nil
}
