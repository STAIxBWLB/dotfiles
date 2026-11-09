package fileutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSameDirectory(t *testing.T) {
	tmp := t.TempDir()
	real := filepath.Join(tmp, "Dropbox")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(tmp, "Box")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		a, b string
		want bool
	}{
		{"identical", real, real, true},
		{"trailing separator", real, real + string(filepath.Separator), true},
		{"unclean spelling", real, real + string(filepath.Separator) + ".", true},
		{"through a symlink", link, real, true},
		{"different folders", real, filepath.Join(tmp, "other"), false},
		{"one side missing", real, "", false},
		{"both missing", "", "", false},
		{"missing but spelled the same", "/no/such/tree", "/no/such/tree", true},
		{"missing and spelled apart", "/no/such/tree", "/no/such/../such/sibling", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := SameDirectory(tc.a, tc.b); got != tc.want {
				t.Errorf("SameDirectory(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}
