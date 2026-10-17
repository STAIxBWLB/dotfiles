package cli

import (
	"bytes"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/syncer"
)

func TestPeerSetupRejectsNonDarwinBeforeMutation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	plist := filepath.Join(home, "Library", "LaunchAgents", "com.dotfiles.peer.plist")
	if err := os.MkdirAll(filepath.Dir(plist), 0o755); err != nil {
		t.Fatal(err)
	}
	const seeded = "seeded before non-darwin rejection"
	if err := os.WriteFile(plist, []byte(seeded), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := newPeerSetupCmdForOS("linux")
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "requires macOS launchd") {
		t.Fatalf("non-darwin error = %v, want macOS launchd guidance", err)
	}
	body, readErr := os.ReadFile(plist)
	if readErr != nil || string(body) != seeded {
		t.Fatalf("non-darwin setup changed plist: body=%q err=%v", body, readErr)
	}
	for _, bootstrapArtifact := range []string{
		filepath.Join(home, "workspace", "work", ".dotfiles", PeerProfile),
		filepath.Join(home, ".config", "dotfiles"),
	} {
		if _, statErr := os.Stat(bootstrapArtifact); !os.IsNotExist(statErr) {
			t.Fatalf("non-darwin setup reached Bootstrap and created %s: %v", bootstrapArtifact, statErr)
		}
	}
}

func TestPrintPeerScheduleDryRunTargetUserGuidance(t *testing.T) {
	for _, off := range []bool{false, true} {
		t.Run(map[bool]string{false: "install", true: "off"}[off], func(t *testing.T) {
			var out, errOut bytes.Buffer
			result := &syncer.PeerScheduleResult{
				Off: off, DryRun: true, TargetUserActionRequired: true,
				Plist:    "/target/Library/LaunchAgents/com.dotfiles.peer.plist",
				Interval: 15 * time.Minute,
			}
			printPeerScheduleDryRun(&Printer{Out: &out, Err: &errOut}, result)

			verb := "write"
			if off {
				verb = "remove"
			}
			if !strings.Contains(out.String(), "dry-run: would "+verb+" "+result.Plist) {
				t.Errorf("stdout missing %s preview: %q", verb, out.String())
			}
			for _, want := range []string{"target-user action required", "no service-manager action ran in the caller domain", "sudo -iu <target-user>"} {
				if !strings.Contains(errOut.String(), want) {
					t.Errorf("stderr missing %q: %q", want, errOut.String())
				}
			}
		})
	}
}

// A started handover ignores SIGHUP: a dropped ssh session must not stop it
// between moving the owner and running this Mac's on_deactivate.
func TestPeerHandoverIgnoresHangup(t *testing.T) {
	goldenSyncFixture(t)
	_, _, _ = runDotForTest("peer", "handover") // fails without a peer; the handler is set first
	if !signal.Ignored(syscall.SIGHUP) {
		t.Fatal("SIGHUP is not ignored during a handover")
	}
}
