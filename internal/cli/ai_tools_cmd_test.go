package cli

import (
	"io"
	"log/slog"
	"reflect"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/aisettings"
	execrun "github.com/STAIxBWLB/dotfiles/internal/exec"
)

func TestRegisteredInstructionToolsFiltersUnregisteredAgents(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := aisettings.NewAgentsManager(execrun.NewRunner(true, logger), t.TempDir(), false)
	got := registeredInstructionTools(mgr, []string{"gencode", "pi", "antigravity", "codex"})
	if !reflect.DeepEqual(got, []string{"pi", "antigravity", "codex"}) {
		t.Fatalf("got %v", got)
	}
	if got := registeredInstructionTools(mgr, []string{"gencode"}); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}
