package toolversion

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSatisfies(t *testing.T) {
	for _, c := range []struct {
		spec, version string
		want          bool
	}{
		{"0.14.0", "0.14.0", true},
		{"0.14.0", "0.17.0", false},
		{">=0.14.0 <1.0.0", "0.14.0", true},
		{">=0.14.0 <1.0.0", "0.17.0", true},
		{">=0.14.0 <1.0.0", "0.13.9", false},
		{">=0.14.0 <1.0.0", "1.0.0", false},
		{">=2.1.283 <3.0.0", "2.1.316", true},
		{">0.2.1", "0.2.1", false},
		{"<=0.2.4", "0.2.4", true},
		{"=0.2.4", "0.2.4", true},
		{">=0.14.0", "0.18.0-rc1", false},
		{">=0.14.0", "", false},
		{"", "0.14.0", false},
		{"~0.14.0", "0.14.0", false},
		{">=0.14", "0.14.0", false},
	} {
		if got := Satisfies(c.spec, c.version); got != c.want {
			t.Errorf("Satisfies(%q, %q) = %v, want %v", c.spec, c.version, got, c.want)
		}
	}
}

// A longer number that merely contains a version is not it, and a prerelease is not the release.
func TestFindTakesTheWholeStableVersionOnly(t *testing.T) {
	for output, want := range map[string]string{
		"0.11.0": "0.11.0", "amem 0.11.0": "0.11.0", "0.11.0\n": "0.11.0", "agent-mem v0.11.0 (bun)": "0.11.0",
		"10.11.0": "10.11.0", "0.11.01": "", "0.11.0.1": "", "0.11": "", "1.0.11.0": "",
		"": "", "amem 0.11.0.": "0.11.0", "0.11.0-rc1": "", "0.11.0+build5": "", "0.11.0 - ok": "0.11.0",
		"codex-cli 0.166.0": "0.166.0", "2.1.316 (Claude Code)": "2.1.316",
	} {
		if got := Find(output); got != want {
			t.Errorf("Find(%q) = %q, want %q", output, got, want)
		}
	}
}

func script(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "tool")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadPrefersThePublishedManifest(t *testing.T) {
	bin := script(t, t.TempDir(), `[ "$1 $2" = "manifest --json" ] || exit 2
echo '{"name":"@staix/agent-mem","version":"0.18.0","supported":">=0.14.0 <1.0.0","mcp":{"tools":["amem_search",{"name":"amem_brief"},7]}}'
`)
	got := Read(context.Background(), bin, "@staix/agent-mem")
	want := Manifest{Version: "0.18.0", Supported: ">=0.14.0 <1.0.0", Tools: []string{"amem_search", "amem_brief"}, Source: "manifest"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// A release without `manifest` (every amem up to 0.17.0) falls back to its package.json version.
func TestReadFallsBackToThePackageJSONVersion(t *testing.T) {
	pkg := filepath.Join(t.TempDir(), "node_modules", "@staix", "agent-mem")
	if err := os.MkdirAll(filepath.Join(pkg, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "package.json"), []byte(`{"name":"@staix/agent-mem","version":"0.17.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	target := script(t, filepath.Join(pkg, "bin"), "echo 'amem: unknown command manifest' >&2; exit 1\n")
	link := filepath.Join(t.TempDir(), "amem")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	got := Read(context.Background(), link, "@staix/agent-mem")
	if !reflect.DeepEqual(got, Manifest{Version: "0.17.0", Source: "package.json"}) {
		t.Fatalf("got %+v", got)
	}
	// Another package's manifest or package.json is never read as this one.
	if got := Read(context.Background(), link, "@staix/vault-mem"); got.Source != "" {
		t.Fatalf("read another package: %+v", got)
	}
}

func TestReadWithNothingPublishedIsZero(t *testing.T) {
	bin := script(t, t.TempDir(), "echo 0.14.0\n")
	if got := Read(context.Background(), bin, "@staix/agent-mem"); !reflect.DeepEqual(got, Manifest{}) {
		t.Fatalf("got %+v", got)
	}
	if got := Read(context.Background(), "", "@staix/agent-mem"); !reflect.DeepEqual(got, Manifest{}) {
		t.Fatalf("got %+v", got)
	}
	foreign := script(t, t.TempDir(), `echo '{"name":"other","version":"9.9.9"}'`+"\n")
	if got := Read(context.Background(), foreign, "@staix/agent-mem"); got.Source != "" {
		t.Fatalf("read a foreign manifest: %+v", got)
	}
}
