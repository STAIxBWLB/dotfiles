package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/fileutil"
	"github.com/STAIxBWLB/dotfiles/internal/syncer"
)

func TestClassifyPeerOutcome(t *testing.T) {
	complete := &syncer.PeerSyncResult{Complete: true}
	for name, tc := range map[string]struct {
		res       *syncer.PeerSyncResult
		runErr    error
		twoWay    bool
		threshold int
		wantKind  peerAlertKind
		wantLevel string
	}{
		"real transfer error is critical": {
			res: complete, runErr: fmt.Errorf("peer plan safety: refusing 2731 outbound deletion(s): over max_delete=2000"),
			twoWay: true, wantKind: peerAlertFailure, wantLevel: "critical",
		},
		"lost lock race stays silent": {
			res: nil, runErr: fmt.Errorf("another peer sync is already running: %w", fileutil.ErrLockHeld),
			twoWay: true, wantKind: peerAlertNone,
		},
		"unreachable peer is designed-clean": {
			res: &syncer.PeerSyncResult{Unreachable: true}, twoWay: true, wantKind: peerAlertNone,
		},
		"demotion is designed-clean": {
			res: &syncer.PeerSyncResult{Demoted: true}, twoWay: true, wantKind: peerAlertNone,
		},
		"demoted with an error still stays silent": {
			res: &syncer.PeerSyncResult{Demoted: true}, runErr: errors.New("hook failed"),
			twoWay: true, wantKind: peerAlertNone,
		},
		"held two-way run warns": {
			res: &syncer.PeerSyncResult{Complete: false}, twoWay: true,
			wantKind: peerAlertFailure, wantLevel: "warn",
		},
		"held one-way run stays silent": {
			res: &syncer.PeerSyncResult{Complete: false}, twoWay: false, wantKind: peerAlertNone,
		},
		"quarantine over threshold warns": {
			res:    &syncer.PeerSyncResult{Complete: true, QuarantinedHere: 40, QuarantinedOnPeer: 20, ConflictStamp: "20261006-120000"},
			twoWay: true, threshold: 50, wantKind: peerAlertFailure, wantLevel: "warn",
		},
		"quarantine at threshold recovers": {
			res:    &syncer.PeerSyncResult{Complete: true, QuarantinedHere: 50},
			twoWay: true, threshold: 50, wantKind: peerAlertRecovery, wantLevel: "info",
		},
		"threshold zero disables the quarantine alert": {
			res:    &syncer.PeerSyncResult{Complete: true, QuarantinedHere: 5000},
			twoWay: true, threshold: 0, wantKind: peerAlertRecovery, wantLevel: "info",
		},
		"clean complete run reports recovery": {
			res: complete, twoWay: true, threshold: 50, wantKind: peerAlertRecovery, wantLevel: "info",
		},
	} {
		t.Run(name, func(t *testing.T) {
			kind, level, msg := classifyPeerOutcome(tc.res, tc.runErr, tc.twoWay, tc.threshold, "m5x26")
			if kind != tc.wantKind || level != tc.wantLevel {
				t.Fatalf("classifyPeerOutcome = (%v, %q, %q), want (%v, %q)", kind, level, msg, tc.wantKind, tc.wantLevel)
			}
			if kind == peerAlertNone && msg != "" {
				t.Errorf("a silent outcome must carry no message, got %q", msg)
			}
			if kind != peerAlertNone && msg == "" {
				t.Error("an alert outcome must carry a message")
			}
		})
	}
}

func TestClassifyPeerOutcome_MessageContent(t *testing.T) {
	// The payload names the machine and the failure class; it must never
	// carry file contents or secret paths (#244).
	kind, _, msg := classifyPeerOutcome(nil, errors.New("rsync exited 23"), true, 0, "m5x26")
	if kind != peerAlertFailure {
		t.Fatalf("kind = %v", kind)
	}
	if got := msg; got != "peer sync failed on m5x26: rsync exited 23" {
		t.Errorf("msg = %q", got)
	}
	_, _, held := classifyPeerOutcome(&syncer.PeerSyncResult{Complete: false}, nil, true, 0, "m5x26")
	if held != "peer sync on m5x26 held destructive transitions; baseline unchanged" {
		t.Errorf("held msg = %q", held)
	}
}

func TestSummarizePeerAlertError(t *testing.T) {
	if got := summarizePeerAlertError(errors.New("first line\nsecond line with detail")); got != "first line" {
		t.Errorf("multi-line error must collapse to its first line, got %q", got)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	if got := summarizePeerAlertError(fmt.Errorf("rsync failed under %s/private/x", home)); strings.Contains(got, home) {
		t.Errorf("the home path must fold to ~ in an outbound alert, got %q", got)
	}
	long := summarizePeerAlertError(errors.New(strings.Repeat("x", 500)))
	if r := []rune(long); len(r) > 200 || !strings.HasSuffix(long, "...") {
		t.Errorf("long errors must cap at 200 runes with an ellipsis, got %d runes", len(r))
	}
}
