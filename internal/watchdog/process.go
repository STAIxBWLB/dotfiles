package watchdog

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

// PSArgs is the column set the reaper samples, kept in one place so the
// parser and the caller can never drift apart. lstart (not etime) anchors
// process identity: etime is truncated to whole seconds, so a start time
// derived from now-etime jitters by a second or two between runs and the
// same live process would keep starting new sustain streaks.
const PSArgs = "pid,ppid,pcpu,lstart,args"

// lstartLayout is the fixed English ctime-style layout ps prints for lstart
// on both macOS and Linux.
const lstartLayout = "Mon Jan 2 15:04:05 2006"

// lstartTokens is how many whitespace-separated fields an lstart value
// occupies in ps output ("Wed Sep 24 10:15:30 2026").
const lstartTokens = 5

// Process is one row of `ps -Ao pid,ppid,pcpu,lstart,args`.
type Process struct {
	PID     int
	PPID    int
	CPU     float64
	Started string // normalized lstart text; identifies the incarnation
	Args    string // full command line
}

// StartKey identifies a process incarnation: the PID alone collides after
// PID reuse, so the exact start time disambiguates. lstart is stable across
// runs, so one live process keeps one key for its whole life.
func (p Process) StartKey() string {
	return fmt.Sprintf("%d@%s", p.PID, p.Started)
}

// Executable returns the first token of the command line.
func (p Process) Executable() string {
	fields := strings.Fields(p.Args)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// ParsePS parses `ps -Ao pid,ppid,pcpu,lstart,args` output, header included.
func ParsePS(output string) ([]Process, error) {
	var procs []Process
	for i, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if i == 0 && strings.HasPrefix(line, "PID") {
			continue
		}
		p, err := parsePSLine(line)
		if err != nil {
			return nil, fmt.Errorf("ps output line %d: %w", i+1, err)
		}
		procs = append(procs, p)
	}
	return procs, nil
}

func parsePSLine(line string) (Process, error) {
	fields := strings.Fields(line)
	if len(fields) < 3+lstartTokens+1 {
		return Process{}, fmt.Errorf("expected at least %d columns, got %d: %q", 3+lstartTokens+1, len(fields), line)
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil {
		return Process{}, fmt.Errorf("bad pid %q", fields[0])
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return Process{}, fmt.Errorf("bad ppid %q", fields[1])
	}
	cpu, err := strconv.ParseFloat(fields[2], 64)
	if err != nil {
		return Process{}, fmt.Errorf("bad pcpu %q", fields[2])
	}
	started := strings.Join(fields[3:3+lstartTokens], " ")
	if _, err := time.Parse(lstartLayout, started); err != nil {
		return Process{}, fmt.Errorf("bad lstart %q", started)
	}
	return Process{
		PID:     pid,
		PPID:    ppid,
		CPU:     cpu,
		Started: started,
		Args:    strings.Join(fields[3+lstartTokens:], " "),
	}, nil
}

// ProcessStartMatches reports whether pid currently exists AND still is the
// incarnation started identifies. The reaper revalidates this before a
// SIGKILL: if the candidate died during the grace window and its PID was
// recycled, the kill belongs to the dead process, not the replacement.
func ProcessStartMatches(ctx context.Context, runner *exec.Runner, pid int, started string) (bool, error) {
	res, err := runner.RunQuery(ctx, "ps", "-o", "lstart=", "-p", strconv.Itoa(pid))
	if res != nil {
		if got := strings.Join(strings.Fields(res.Stdout), " "); got != "" {
			return got == started, nil
		}
	}
	if err != nil {
		// ps exits 1 when the pid is gone: that is "no match", not a failure.
		var cmdErr *exec.CmdError
		if errors.As(err, &cmdErr) && cmdErr.ExitCode == 1 {
			return false, nil
		}
		return false, err
	}
	return false, nil
}
