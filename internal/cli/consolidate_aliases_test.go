package cli

import (
	"os"
	"strings"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/config"
)

// init --detect resolves the target home like the wizard: $DOTFILES_HOME
// counts when --home is omitted, so --force never writes the invoking
// user's config.
func TestInitDetectHonorsDotfilesHome(t *testing.T) {
	target := t.TempDir()
	t.Setenv("DOTFILES_HOME", target)
	out, errOut, err := runDotForTest("init", "--detect", "--force")
	if err != nil {
		t.Fatalf("init --detect: %v\n%s", err, errOut)
	}
	want := config.StatePathForHome(target)
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("state not written under DOTFILES_HOME (%s): %v\n%s", want, err, out)
	}
	if !strings.Contains(out, want) {
		t.Fatalf("summary does not name %s:\n%s", want, out)
	}
}
