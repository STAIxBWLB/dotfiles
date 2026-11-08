package cli

import (
	"strings"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/config"
)

// #378 AC1: each old name still works for one release, names its
// replacement on stderr only, and prints the same report on stdout.
func TestRenamedCommandsKeepWorkingAndNameReplacement(t *testing.T) {
	home := t.TempDir()
	if err := config.SaveStateForHome(home, &config.UserState{Name: "N", Email: "n@example.invalid", Profile: "minimal"}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		old, new []string
		want     string
	}{
		{[]string{"check"}, []string{"diff", "--summary"}, "dot diff --summary"},
		{[]string{"doctor"}, []string{"layouts", "doctor"}, "dot layouts doctor"},
		{[]string{"preflight", "--check-only"}, []string{"init", "--check-only"}, "dot init --check-only"},
	} {
		t.Run(strings.Join(tc.old, " "), func(t *testing.T) {
			oldOut, oldErr, err := runDotForTest(append([]string{"--home", home, "--module", "git"}, tc.old...)...)
			if err != nil {
				t.Fatalf("old name failed: %v\n%s", err, oldErr)
			}
			if !strings.Contains(oldErr, tc.want) || strings.Contains(oldOut, "deprecated") {
				t.Fatalf("stderr must name %q and stdout must stay clean\nstdout=%s\nstderr=%s", tc.want, oldOut, oldErr)
			}
			newOut, newErr, err := runDotForTest(append([]string{"--home", home, "--module", "git"}, tc.new...)...)
			if err != nil {
				t.Fatalf("new name failed: %v\n%s", err, newErr)
			}
			if strings.Contains(newErr, "deprecated") {
				t.Fatalf("new name printed a deprecation: %s", newErr)
			}
			if firstLine(oldOut) != firstLine(newOut) {
				t.Fatalf("reports differ:\nold=%s\nnew=%s", oldOut, newOut)
			}
		})
	}
}

// preflight without --check-only maps to init --detect and writes the config.
func TestPreflightAliasDetectWritesConfig(t *testing.T) {
	home := t.TempDir()
	out, errOut, err := runDotForTest("--home", home, "preflight")
	if err != nil {
		t.Fatalf("preflight: %v\n%s", err, errOut)
	}
	if !strings.Contains(errOut, "dot init --detect") {
		t.Fatalf("stderr does not name dot init --detect: %s", errOut)
	}
	if !strings.Contains(out, "Generated config.") {
		t.Fatalf("no generated config summary:\n%s", out)
	}
	out, _, err = runDotForTest("--home", home, "init", "--detect")
	if err != nil || !strings.Contains(out, "Config already exists") {
		t.Fatalf("init --detect should keep an existing config without --force: %v\n%s", err, out)
	}
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
