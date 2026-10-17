package admission

import (
	"context"
	"fmt"
	"os"
	"os/user"
	"strconv"
	"strings"
	"syscall"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

// UserStateRoot is the admission state root of the real user. Slots are per
// user and repository, so an alternate --home must not open a second slot
// for the same repository (#162).
func UserStateRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving the admission state root: %w", err)
	}
	return DefaultStateRoot(home), nil
}

// SelfLease fills the process-identity fields of the lease this run
// would hold. PIDStart is normalized exactly the way the watchdog compares
// it, so a later stale-owner probe matches this incarnation. It fails
// closed: without a verified start time the lease's identity is
// unverifiable, and an unverifiable lease could later be reclaimed from a
// live owner — so no lease is better than a guess.
func SelfLease(ctx context.Context, runner *exec.Runner, cwd string) (Lease, error) {
	owner := os.Getenv(OwnerEnv)
	if owner == "" {
		username := "unknown"
		if u, err := user.Current(); err == nil {
			username = u.Username
		}
		host, _ := os.Hostname()
		owner = username + "@" + host
	}
	started, err := ProcessStart(ctx, runner, os.Getpid())
	if err != nil {
		return Lease{}, fmt.Errorf("cannot verify this process's start time (%v); refusing to acquire a slot with an unverifiable identity", err)
	}
	pgid, _ := syscall.Getpgid(os.Getpid())
	return Lease{
		Owner:    owner,
		Session:  os.Getenv(SessionEnv),
		PID:      os.Getpid(),
		PIDStart: started,
		PGID:     pgid,
		CWD:      cwd,
	}, nil
}

// ProcessStart reads one process's lstart, normalized the way
// watchdog.ProcessStartMatches compares it. An empty result is an error:
// it would record an unverifiable identity.
func ProcessStart(ctx context.Context, runner *exec.Runner, pid int) (string, error) {
	res, err := runner.RunQuery(ctx, "ps", "-o", "lstart=", "-p", strconv.Itoa(pid))
	if err != nil {
		return "", err
	}
	started := strings.Join(strings.Fields(res.Stdout), " ")
	if started == "" {
		return "", fmt.Errorf("ps returned no start time for pid %d", pid)
	}
	return started, nil
}
