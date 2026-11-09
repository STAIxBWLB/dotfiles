package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --include-age-key copies the age key silently; the removed
// --include-secrets name is rejected (#377, #394).
func TestIncludeAgeKeyCopiesAgeKey(t *testing.T) {
	f := newOnestopFixture(t)
	out, errOut, err := runDotForTest("backup", "--yes", "--to", f.root, "--scope", "profile", "--include-age-key")
	if err != nil {
		t.Fatalf("backup: %v\nstderr=%s", err, errOut)
	}
	host := filepath.Join(f.root, "profiles", f.host)
	if _, err := os.Stat(filepath.Join(host, latestVersion(t, host), "secrets", "age_key")); err != nil {
		t.Fatalf("age key not copied: %v", err)
	}
	if strings.Contains(out+errOut, "deprecated") {
		t.Fatalf("unexpected deprecation\nstdout=%s\nstderr=%s", out, errOut)
	}
}

func TestIncludeSecretsFlagRemoved(t *testing.T) {
	f := newOnestopFixture(t)
	if _, _, err := runDotForTest("backup", "--yes", "--to", f.root, "--scope", "profile", "--include-secrets"); err == nil {
		t.Fatal("--include-secrets must be an unknown flag")
	}
}
