package cli

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	dotexec "github.com/STAIxBWLB/dotfiles/internal/exec"
	"github.com/STAIxBWLB/dotfiles/internal/fileutil"
)

// TestScheduledRunEnv_MatchesThePlist pins the marker's name from the cli
// side. The syncer side asserts the same literal appears in the launchd
// plist template; nothing else binds the two packages, so both halves have
// to name the value rather than share a symbol.
func TestScheduledRunEnv_MatchesThePlist(t *testing.T) {
	if scheduledRunEnv != "DOT_SCHEDULED_RUN" {
		t.Fatalf("scheduledRunEnv = %q; the plist template hardcodes DOT_SCHEDULED_RUN", scheduledRunEnv)
	}
}

// TestQuietScheduledContention is BUG-04's scheduled-run clause, including
// both non-vacuity rows. Without them the handler could unconditionally
// swallow and every row below would still pass.
func TestQuietScheduledContention(t *testing.T) {
	lockDir := "/tmp/example/.dot-lock"
	contention := fmt.Errorf("another peer sync is already running: %w",
		fmt.Errorf("another sync is running (lock: %s): %w", lockDir, fileutil.ErrLockHeld))
	unreachable := errors.New("peer target is not an ssh target; run: dot peer init --host <user@host>")

	for _, tc := range []struct {
		name      string
		scheduled bool
		err       error
		wantErr   error
		wantLog   bool
	}{
		{
			name:      "scheduled run swallows lock contention",
			scheduled: true,
			err:       contention,
			wantErr:   nil,
			wantLog:   true,
		},
		{
			name:      "an interactive run still fails on contention",
			scheduled: false,
			err:       contention,
			wantErr:   contention,
		},
		{
			name:      "a scheduled run stays loud about a real failure",
			scheduled: true,
			err:       unreachable,
			wantErr:   unreachable,
		},
		{
			name:      "success is untouched",
			scheduled: true,
			err:       nil,
			wantErr:   nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.scheduled {
				t.Setenv(scheduledRunEnv, "1")
			} else {
				t.Setenv(scheduledRunEnv, "")
			}
			var logs bytes.Buffer
			runner := dotexec.NewRunner(false, slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo})))

			got := quietScheduledContention(runner, tc.err)
			if tc.wantErr == nil {
				if got != nil {
					t.Fatalf("err = %v, want nil", got)
				}
			} else if !errors.Is(got, tc.wantErr) {
				t.Fatalf("err = %v, want %v", got, tc.wantErr)
			}
			logged := strings.Contains(logs.String(), lockDir)
			if logged != tc.wantLog {
				t.Fatalf("logged=%v want %v; a swallowed run must still leave a record.\n%s", logged, tc.wantLog, logs.String())
			}
		})
	}
}
