package cli

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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
	// Mirror macOS /var -> /private/var and other symlinked HOME layouts.
	realHome := t.TempDir()
	home := filepath.Join(t.TempDir(), "home")
	if err := os.Symlink(realHome, home); err != nil {
		t.Fatal(err)
	}
	userHome := t.TempDir()
	t.Setenv("HOME", userHome)
	t.Setenv("GITHUB_TOKEN", "")
	old := buildVersionedDotFixture(t, "1.0.0")
	selected := buildVersionedDotFixture(t, "2.0.0")
	third := buildVersionedDotFixture(t, "3.0.0")
	fourth := buildVersionedDotFixture(t, "4.0.0")
	stable := filepath.Join(home, ".local", "bin", "dot")
	if err := os.MkdirAll(filepath.Dir(stable), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stable, old, 0o755); err != nil {
		t.Fatal(err)
	}
	userBinary := filepath.Join(userHome, ".local", "bin", "dot")
	if err := os.MkdirAll(filepath.Dir(userBinary), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userBinary, old, 0o755); err != nil {
		t.Fatal(err)
	}
	executable, transport, prebuilt, acquire, processes := upgradeExecutable, http.DefaultTransport, upgradeAcquirePrebuilt, upgradeAcquire, dotInstallProcesses
	t.Cleanup(func() {
		upgradeExecutable, http.DefaultTransport, upgradeAcquirePrebuilt, upgradeAcquire, dotInstallProcesses = executable, transport, prebuilt, acquire, processes
	})
	upgradeExecutable = func() (string, error) { return userBinary, nil }
	dotInstallProcesses = func(context.Context) (string, error) { return "", nil }
	run := func(current string, args ...string) (string, error) {
		return executeDotFixtureCommand(t, current, append(args, "--home", home)...)
	}
	acquired := 0
	upgradeAcquirePrebuilt = func(context.Context) (func(), error) { acquired++; return func() {}, nil }
	upgradeAcquire = func(context.Context, resourceguard.Options) (func(), error) { acquired++; return func() {}, nil }
	tr := &dotVersionTransport{archives: map[string][]byte{}}
	for version, data := range map[string][]byte{"1.0.0": old, "2.0.0": selected, "3.0.0": third, "4.0.0": fourth} {
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
	if out, err := run("1.0.0", "update", "--version", "v2.0.0"); err != nil {
		t.Fatalf("selected update: %v\n%s", err, out)
	}
	assertHook("2.0.0")
	// Newly registered hooks must retain current, not resolve it to 2.0.0.
	managedHook := guard.HookCommand(resolveGuardDotPath(home))
	if !strings.Contains(managedHook, "/current/bin/dot") {
		t.Fatalf("hook resolved the version link: %s", managedHook)
	}
	if out, err := run("2.0.0", "rollback"); err != nil {
		t.Fatalf("rollback: %v\n%s", err, out)
	}
	assertHook("1.0.0")
	if out, err := exec.Command("sh", "-c", managedHook).CombinedOutput(); err != nil || strings.TrimSpace(string(out)) != "guard-1.0.0" {
		t.Fatalf("post-update hook did not follow rollback: %q, %v", out, err)
	}
	if _, err := run("1.0.0", "rollback"); err != nil {
		t.Fatal(err)
	}
	assertHook("2.0.0")
	if _, err := run("2.0.0", "update", "--version", "1.0.0"); err != nil {
		t.Fatalf("explicit downgrade: %v", err)
	}
	assertHook("1.0.0")
	for version, want := range map[string][]byte{"1.0.0": old, "2.0.0": selected} {
		got, err := os.ReadFile(standaloneDotBinary(home, version))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("immutable version %s changed: %v", version, err)
		}
	}
	// Three successive newer releases retain only N and N-1 once idle.
	if _, err := run("1.0.0", "update", "--version", "3.0.0"); err != nil {
		t.Fatal(err)
	}
	if _, err := run("3.0.0", "update", "--version", "4.0.0"); err != nil {
		t.Fatal(err)
	}
	assertHook("4.0.0")
	versions, err := dotVersions(home)
	if err != nil || !slices.Equal(versions, []string{"3.0.0", "4.0.0"}) {
		t.Fatalf("idle retention = %v, %v; want current and previous only", versions, err)
	}
	if acquired != 6 {
		t.Fatalf("maintenance acquisitions = %d, want 6", acquired)
	}
	if got, err := os.ReadFile(userBinary); err != nil || !bytes.Equal(got, old) {
		t.Fatalf("user HOME executable changed: %v", err)
	}
	if st, err := os.Lstat(userBinary); err != nil || !st.Mode().IsRegular() {
		t.Fatalf("user HOME executable replaced with a link: %v", err)
	}
	if _, err := os.Lstat(standaloneDotRoot(userHome)); !os.IsNotExist(err) {
		t.Fatalf("update wrote the user HOME versions tree: %v", err)
	}
	// make install replaces the PATH link, leaving saved current behind. The
	// next update must preserve that flat build and rollback must restore it.
	dev := buildVersionedDotFixture(t, "dev")
	flat := filepath.Join(filepath.Dir(stable), ".dot.make-install")
	if err := os.WriteFile(flat, dev, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(flat, stable); err != nil {
		t.Fatal(err)
	}
	if got := resolveGuardDotPath(home); got != stable {
		t.Fatalf("stale saved current won over flat PATH binary: %s", got)
	}
	if _, err := run("dev", "update", "--version", "3.0.0"); err != nil {
		t.Fatal(err)
	}
	previous, err := dotVersionLink(home, "previous")
	if err != nil || previous != fmt.Sprintf("snapshot-%x", sha256.Sum256(dev)) {
		t.Fatalf("flat predecessor lost: %s, %v", previous, err)
	}
	if _, err := run("3.0.0", "rollback"); err != nil {
		t.Fatal(err)
	}
	assertHook("dev")
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
		if err == nil || !strings.Contains(err.Error(), "owned by Homebrew") || !strings.Contains(err.Error(), "brew") || !strings.Contains(err.Error(), "extract --version") {
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

func TestDotPruneRetainsInUseAndUnresolvedVersions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, version := range []string{"1.0.0", "2.0.0", "3.0.0"} {
		if err := stageDotBinary(home, version, []byte(version)); err != nil {
			t.Fatal(err)
		}
	}
	original := dotInstallProcesses
	t.Cleanup(func() { dotInstallProcesses = original })
	old := standaloneDotBinary(home, "1.0.0")
	stable := filepath.Join(home, ".local", "bin", "dot")
	var output bytes.Buffer
	p := &Printer{Out: &output}
	dotInstallProcesses = func(context.Context) (string, error) { return "4242 " + old + " guard hook\n", nil }
	pruneDotVersions(context.Background(), p, home, true, "3.0.0", "2.0.0", "")
	if _, err := os.Stat(old); err != nil || !strings.Contains(output.String(), "kept dot 1.0.0: a running process uses it") {
		t.Fatalf("in-use N-2 lost or note missing: %v, %s", err, output.String())
	}
	output.Reset()
	dotInstallProcesses = func(context.Context) (string, error) { return "4242 " + stable + " guard hook\n", nil }
	pruneDotVersions(context.Background(), p, home, true, "3.0.0", "2.0.0", "")
	if _, err := os.Stat(old); err != nil || !strings.Contains(output.String(), "cannot be tied to one version") {
		t.Fatalf("ambiguous process was not conservative: %v, %s", err, output.String())
	}
	output.Reset()
	dotInstallProcesses = func(context.Context) (string, error) { return "", errors.New("ps unavailable") }
	pruneDotVersions(context.Background(), p, home, true, "3.0.0", "2.0.0", "")
	if _, err := os.Stat(old); err != nil || !strings.Contains(output.String(), "process inspection failed") {
		t.Fatalf("missing process evidence pruned a version: %v, %s", err, output.String())
	}
	dotInstallProcesses = func(context.Context) (string, error) { return "", nil }
	pruneDotVersions(context.Background(), p, home, true, "3.0.0", "2.0.0", "")
	versions, err := dotVersions(home)
	if err != nil || !slices.Equal(versions, []string{"2.0.0", "3.0.0"}) {
		t.Fatalf("idle prune = %v, %v", versions, err)
	}
}

func TestGuardPathPrefersHomebrewOverStaleSavedCurrent(t *testing.T) {
	home := t.TempDir()
	if err := stageDotBinary(home, "1.0.0", []byte("stale saved binary")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("1.0.0", filepath.Join(standaloneDotRoot(home), "current")); err != nil {
		t.Fatal(err)
	}
	cellar := filepath.Join(home, "brew", "Cellar", "dotfiles", "2.0.0", "bin", "dot")
	if err := os.MkdirAll(filepath.Dir(cellar), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cellar, []byte("active brew binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	stable := filepath.Join(home, ".local", "bin", "dot")
	if err := os.MkdirAll(filepath.Dir(stable), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(cellar, stable); err != nil {
		t.Fatal(err)
	}
	physicalHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(physicalHome, "brew", "opt", "dotfiles", "bin", "dot")
	if got := resolveGuardDotPath(home); got != want {
		t.Fatalf("guard uses %s, want owning brew %s", got, want)
	}
}
