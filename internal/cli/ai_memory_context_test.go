package cli

import (
	"context"
	"testing"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/aisettings"
	"github.com/spf13/cobra"
)

func TestMemoryBridgeUsesCommandCancellationWithoutStartingWorker(t *testing.T) {
	mgr := aisettings.NewClaudeMemManager(t.TempDir(), "", "")
	mgr.SelectedAgents = []string{}
	// Explicit-empty selection does not require or execute a native runtime.
	mgr.BunPath = "/nonexistent/bun"
	cmd := newAIMemoryBridgeCmdWithManager(func(*cobra.Command) (*aisettings.ClaudeMemManager, error) { return mgr, nil })
	cmd.SetArgs([]string{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- cmd.ExecuteContext(ctx) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("bridge ignored command cancellation")
	}
}
