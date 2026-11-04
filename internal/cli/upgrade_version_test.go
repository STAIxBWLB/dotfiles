package cli

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/guard"
	"github.com/STAIxBWLB/dotfiles/internal/resourceguard"
)

type dotVersionTransport struct {
	archives map[string][]byte
	requests []string
}

func (tr *dotVersionTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	tr.requests = append(tr.requests, req.URL.Path)
	for version, archive := range tr.archives {
		if !strings.Contains(req.URL.Path, "/v"+version+"/") {
			continue
		}
		body := archive
		if strings.HasSuffix(req.URL.Path, "checksums.txt") {
			goos, goarch, err := supportedNativeReleaseTarget()
			if err != nil {
				return nil, err
			}
			body = []byte(fmt.Sprintf("%x  dot_%s_%s_%s.tar.gz\n", sha256.Sum256(archive), version, goos, goarch))
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body)), Request: req}, nil
	}
	return nil, fmt.Errorf("unexpected network request: %s", req.URL)
}

// Real native fixtures exercise the unchanged binary-target and runtime version
// checks. Only these temp-HOME fixtures are executed or installed, never dot.
func buildVersionedDotFixture(t *testing.T, version string) []byte {
	t.Helper()
	dir := t.TempDir()
	source := filepath.Join(dir, "main.go")
	program := fmt.Sprintf(`package main
import ("fmt"; "os")
func main() {
 if len(os.Args) == 2 && os.Args[1] == "--version" { fmt.Println("dot version %s (fixture)"); return }
 if len(os.Args) == 3 && os.Args[1] == "guard" && os.Args[2] == "hook" { fmt.Println("guard-%s"); return }
 os.Exit(2)
}
`, version, version)
	if err := os.WriteFile(source, []byte(program), 0o600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(dir, "dot")
	cmd := exec.Command("go", "build", "-p", "2", "-o", binary, source)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build native fixture: %v\n%s", err, out)
	}
	data, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func executeDotFixtureCommand(t *testing.T, current string, args ...string) (string, error) {
	t.Helper()
	cmd := NewRootCmd(current, "fixture")
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestDotSelectedReleaseRollbackAndStableGuard(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GITHUB_TOKEN", "")
	old := buildVersionedDotFixture(t, "1.0.0")
	selected := buildVersionedDotFixture(t, "2.0.0")
	stable := filepath.Join(home, ".local", "bin", "dot")
	if err := os.MkdirAll(filepath.Dir(stable), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stable, old, 0o755); err != nil {
		t.Fatal(err)
	}
	executable, transport, prebuilt, acquire := upgradeExecutable, http.DefaultTransport, upgradeAcquirePrebuilt, upgradeAcquire
	t.Cleanup(func() {
		upgradeExecutable, http.DefaultTransport, upgradeAcquirePrebuilt, upgradeAcquire = executable, transport, prebuilt, acquire
	})
	upgradeExecutable = func() (string, error) { return stable, nil }
	acquired := 0
	upgradeAcquirePrebuilt = func(context.Context) (func(), error) { acquired++; return func() {}, nil }
	upgradeAcquire = func(context.Context, resourceguard.Options) (func(), error) { acquired++; return func() {}, nil }
	tr := &dotVersionTransport{archives: map[string][]byte{}}
	for version, data := range map[string][]byte{"1.0.0": old, "2.0.0": selected} {
		tr.archives[version] = tarGzFixture(t, []tar.Header{{Name: "dot", Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(data))}}, [][]byte{data})
	}
	http.DefaultTransport = tr
	hook := guard.HookCommand(resolveGuardDotPath(home))
	assertHook := func(version string) {
		t.Helper()
		out, err := exec.Command("sh", "-c", hook).CombinedOutput()
		if err != nil || strings.TrimSpace(string(out)) != "guard-"+version {
			t.Fatalf("unchanged hook %q: %q, %v", hook, out, err)
		}
		got, err := readDotBinaryVersion(context.Background(), stable)
		if err != nil || got != version {
			t.Fatalf("PATH version = %q, %v; want %s", got, err, version)
		}
	}
	assertHook("1.0.0")
	if out, err := executeDotFixtureCommand(t, "1.0.0", "update", "--version", "v2.0.0"); err != nil {
		t.Fatalf("selected update: %v\n%s", err, out)
	}
	assertHook("2.0.0")
	// Newly registered hooks must retain current, not resolve it to 2.0.0.
	managedHook := guard.HookCommand(resolveGuardDotPath(home))
	if !strings.Contains(managedHook, "/current/bin/dot") {
		t.Fatalf("hook resolved the version link: %s", managedHook)
	}
	if out, err := executeDotFixtureCommand(t, "2.0.0", "rollback"); err != nil {
		t.Fatalf("rollback: %v\n%s", err, out)
	}
	assertHook("1.0.0")
	if out, err := exec.Command("sh", "-c", managedHook).CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != "guard-1.0.0" {
		t.Fatalf("post-update hook did not follow rollback: %q, %v", out, err)
	}
	if _, err := executeDotFixtureCommand(t, "1.0.0", "rollback"); err != nil {
		t.Fatal(err)
	}
	assertHook("2.0.0")
	if _, err := executeDotFixtureCommand(t, "2.0.0", "update", "--version", "1.0.0"); err != nil {
		t.Fatalf("explicit downgrade: %v", err)
	}
	assertHook("1.0.0")
	for version, want := range map[string][]byte{"1.0.0": old, "2.0.0": selected} {
		got, err := os.ReadFile(standaloneDotBinary(home, version))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("immutable version %s changed: %v", version, err)
		}
	}
	if acquired != 4 {
		t.Fatalf("maintenance acquisitions = %d, want 4", acquired)
	}
	for _, request := range tr.requests {
		if strings.Contains(request, "/latest") || strings.Contains(request, "api.github.com") {
			t.Fatalf("explicit version queried latest: %s", request)
		}
	}
}

func TestDotVersionControlRefusesHomebrewAndInvalidVersions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cellar := filepath.Join(home, "brew", "Cellar", "dotfiles", "1.0.0", "bin", "dot")
	if err := os.MkdirAll(filepath.Dir(cellar), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cellar, []byte("unchanged cellar bytes"), 0o755); err != nil {
		t.Fatal(err)
	}
	prev, transport := upgradeExecutable, http.DefaultTransport
	t.Cleanup(func() { upgradeExecutable, http.DefaultTransport = prev, transport })
	upgradeExecutable = func() (string, error) { return cellar, nil }
	tr := &dotVersionTransport{}
	http.DefaultTransport = tr
	for _, args := range [][]string{{"update", "--version", "2.0.0"}, {"rollback"}} {
		_, err := executeDotFixtureCommand(t, "1.0.0", args...)
		if err == nil || !strings.Contains(err.Error(), "Homebrew owns") || !strings.Contains(err.Error(), "brew") || !strings.Contains(err.Error(), "extract --version") {
			t.Fatalf("%v should name cause and brew next step: %v", args, err)
		}
	}
	for _, version := range []string{"", "../bad", "v1.0.0-rc.1", "1.2"} {
		if _, err := executeDotFixtureCommand(t, "1.0.0", "update", "--version", version); err == nil {
			t.Fatalf("accepted invalid version %q", version)
		}
	}
	if len(tr.requests) != 0 {
		t.Fatalf("refused version control contacted network: %v", tr.requests)
	}
	got, _ := os.ReadFile(cellar)
	if string(got) != "unchanged cellar bytes" {
		t.Fatal("Homebrew Cellar was modified")
	}
}
