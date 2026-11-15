package cli

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
)

// TestNewAgentsManagerFromCmd_CarriesCommandWriter closes the gap between the
// SEAM-01 carrier and its production caller. AgentsManager.Out exists so that
// authorInteractive's prompts and progress lines follow the command's writer,
// but the seam is inert until the constructor site assigns it: a nil Out
// resolves to os.Stdout, so a caller that redirects cobra's output (a test
// buffer, an embedding caller) silently loses every one of those lines.
//
// newAgentsManagerFromCmd is the single construction choke point for all
// twelve `dot ai` call sites, so asserting here covers them all. Deleting the
// mgr.Out assignment must turn this test red.
func TestNewAgentsManagerFromCmd_CarriesCommandWriter(t *testing.T) {
	cmd := &cobra.Command{Use: "author"}
	cmd.Flags().Bool("dry-run", false, "")
	cmd.Flags().String("home", t.TempDir(), "")
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	mgr := newAgentsManagerFromCmd(cmd)
	if mgr.Out != &buf {
		t.Errorf("AgentsManager.Out = %v, want the command's writer %v", mgr.Out, &buf)
	}
}

// TestAIAgentsPathStaysOneUnwrappedLine pins the script contract: a caller
// reads this value back with $(dot ai agents path), so it has to be one line
// whatever width the report renderer would otherwise wrap to. Writing it with
// Printer.Line wraps it once COLUMNS is narrow, which is the bug this pins.
func TestAIAgentsPathStaysOneUnwrappedLine(t *testing.T) {
	home := t.TempDir()
	t.Setenv("COLUMNS", "20")
	out, errOut, err := runDotForTest("--home", home, "ai", "agents", "path")
	if err != nil {
		t.Fatalf("ai agents path: %v (stderr=%s)", err, errOut)
	}
	want := filepath.Join(home, ".config", "dotfiles", "agents") + "\n"
	if out != want {
		t.Fatalf("ai agents path = %q, want %q: a script reads this value verbatim, so it must not be wrapped", out, want)
	}
}
