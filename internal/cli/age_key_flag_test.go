package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// #377 AC1: --include-secrets still copies the age key, warns on stderr only
// and names --include-age-key; the new flag copies the same files silently.
func TestIncludeSecretsAliasStillCopiesAgeKey(t *testing.T) {
	for _, tc := range []struct {
		flag     string
		wantWarn bool
	}{
		{"--include-age-key", false},
		{"--include-secrets", true},
	} {
		t.Run(tc.flag, func(t *testing.T) {
			f := newOnestopFixture(t)
			out, errOut, err := runDotForTest("backup", "--yes", "--to", f.root, "--scope", "profile", tc.flag)
			if err != nil {
				t.Fatalf("backup: %v\nstderr=%s", err, errOut)
			}
			host := filepath.Join(f.root, "profiles", f.host)
			if _, err := os.Stat(filepath.Join(host, latestVersion(t, host), "secrets", "age_key")); err != nil {
				t.Fatalf("age key not copied with %s: %v", tc.flag, err)
			}
			warned := strings.Contains(errOut, "--include-secrets is deprecated") && strings.Contains(errOut, "--include-age-key")
			if warned != tc.wantWarn || strings.Contains(out, "deprecated") {
				t.Fatalf("warning mismatch for %s (want %v)\nstdout=%s\nstderr=%s", tc.flag, tc.wantWarn, out, errOut)
			}
		})
	}
}
