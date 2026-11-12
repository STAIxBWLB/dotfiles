package remote

import (
	"context"
	"io"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/admission"
	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

func TestPortsPublishOnLoopbackAndForwardOverTheRunsSSH(t *testing.T) {
	h := testHost()
	argv := strings.Join(ContainerArgvWith(h, "c", "one", []string{"serve"}, RunExtras{Ports: []int{8080, 9000}}), " ")
	for _, want := range []string{"-p 127.0.0.1:8080:8080", "-p 127.0.0.1:9000:9000"} {
		if !strings.Contains(argv, want) {
			t.Errorf("container argv lacks %q: %s", want, argv)
		}
	}
	if strings.Contains(argv, "-p 8080") || strings.Contains(argv, "0.0.0.0") {
		t.Errorf("a port is published beyond the host loopback: %s", argv)
	}
	ssh := RunSSHArgv(h, []int{8080}, "cmd")
	want := []string{"ssh", "-o", "BatchMode=yes", "-o", "ExitOnForwardFailure=yes", "-L", "127.0.0.1:8080:127.0.0.1:8080", "buildbox", "cmd"}
	if strings.Join(ssh, " ") != strings.Join(want, " ") {
		t.Errorf("ssh argv = %v", ssh)
	}
	if got := RunSSHArgv(h, nil, "cmd"); strings.Join(got, " ") != strings.Join(SSHArgv(h.SSH, "cmd"), " ") {
		t.Errorf("a run without ports changed its ssh argv: %v", got)
	}
}

func TestCgroupRunsSetupAsRootThenDropsEveryCapability(t *testing.T) {
	h := testHost()
	cmd := RunCommandWith(h, "c", "one", []string{"make", "test"}, RunExtras{Cgroup: true})
	for _, want := range []string{
		"DOT_REMOTE_USER=$(docker image inspect -f '{{.Config.User}}'",
		`-e DOT_REMOTE_USER="$DOT_REMOTE_USER"`,
		"--user 0", "--cap-add SYS_ADMIN", "--security-opt apparmor=unconfined", "--cgroupns private",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("cgroup command lacks %q:\n%s", want, cmd)
		}
	}
	script := CgroupScript([]string{"make", "test"})
	for _, want := range []string{"--inh-caps=-all", "--bounding-set=-all", `--reuid="$DOT_REMOTE_USER"`, `DOT_REMOTE_CGROUP="$c/delegated"`, "cgroup.subtree_control", "'make test'"} {
		if !strings.Contains(script, want) {
			t.Errorf("cgroup script lacks %q", want)
		}
	}
	if strings.Index(script, "cgroup.subtree_control") > strings.Index(script, "setpriv") {
		t.Error("the drop comes before the setup")
	}
	if strings.Contains(RunCommand(h, "c", "one", []string{"true"}), "SYS_ADMIN") {
		t.Error("a run without --cgroup gets CAP_SYS_ADMIN")
	}
	if out, err := osexec.Command("bash", "-n", "-c", script).CombinedOutput(); err != nil {
		t.Errorf("cgroup script does not parse: %v %s", err, out)
	}
}

// pruneFixture answers the list script with canned lines and records the
// removal scripts.
type pruneFixture struct {
	listing string
	calls   []string
}

func (f *pruneFixture) deps(root string) Deps {
	return Deps{
		Store:    admission.NewStore(filepath.Join(root, "admission"), exec.NewProbeRunner()),
		SentRoot: filepath.Join(root, "sent"),
		Exec: func(_ context.Context, argv []string, _ io.Reader, stdout, _ io.Writer) (int, syscall.Signal, error) {
			last := argv[len(argv)-1]
			if strings.Contains(last, "for-each-ref") {
				_, _ = io.WriteString(stdout, f.listing)
				return 0, 0, nil
			}
			f.calls = append(f.calls, last)
			return 0, 0, nil
		},
	}
}

func TestPruneListsFirstAndRemovesOnlyWithYes(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "work", "here"), 0o755); err != nil {
		t.Fatal(err)
	}
	h := testHost()
	cfg := Config{LocalRoot: root, Hosts: map[string]Host{h.Name: h}}
	f := &pruneFixture{listing: strings.Join([]string{
		"mirror\t" + MirrorKey("work/gone") + "\tfree\twork/gone",
		"mirror\t" + MirrorKey("work/here") + "\tfree\twork/here",
		"mirror\t" + MirrorKey("work/busy") + "\tclaimed\twork/busy",
		"mirror\tdeadbeef0000\tfree\t../escape",
		"ref\trepo-1.git\trefs/verify/abc/123-n",
		"container\tme-rdev-0123\tExited (137) 2 hours ago",
		"job\tmedia\t/data/jobs/broken",
	}, "\n")}
	d := f.deps(root)
	report, err := d.Prune(context.Background(), cfg, h, PruneOptions{})
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]string{}
	for _, item := range report.Items {
		kinds[item.Kind] = item.Name
	}
	if kinds["mirror"] != "work/gone" || kinds["verify-ref"] != "repo-1.git refs/verify/abc/123-n" || kinds["container"] != "me-rdev-0123" || len(report.Items) != 3 {
		t.Errorf("items = %+v", report.Items)
	}
	if len(report.Skipped) != 2 || len(report.Kept) != 1 {
		t.Errorf("skipped %v kept %v", report.Skipped, report.Kept)
	}
	if len(f.calls) != 0 {
		t.Errorf("a listing removed something: %v", f.calls)
	}

	report, err = d.Prune(context.Background(), cfg, h, PruneOptions{Yes: true})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(f.calls, "\n")
	for _, want := range []string{
		`mkdir "$lock"`, `target="$HOME"/'dot-remote/work/gone'`,
		"update-ref -d 'refs/verify/abc/123-n'", "name=^me-rdev-verify-abc$",
		"docker rm 'me-rdev-0123'",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("removals lack %q:\n%s", want, joined)
		}
	}
	for _, keep := range []string{"work/here", "work/busy", "escape", "/data/jobs"} {
		if strings.Contains(joined, keep) {
			t.Errorf("prune removed %s, which it must keep:\n%s", keep, joined)
		}
	}
	for _, item := range report.Items {
		if !item.Removed {
			t.Errorf("%+v not removed", item)
		}
	}
}

// The listing and the mirror removal run on a real shell against a fake home.
func TestPruneScriptsOnAShell(t *testing.T) {
	home := t.TempDir()
	h := testHost()
	records := filepath.Join(home, h.Root, ".dot-remote")
	if err := os.MkdirAll(records, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"work/gone", "work/busy"} {
		if err := os.MkdirAll(filepath.Join(home, h.Root, rel), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(records, MirrorKey(rel)), []byte(rel), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(records, MirrorKey("work/busy")+".claim"), 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(script string) string {
		c := osexec.Command("bash", "-c", "docker(){ :; }; "+script)
		c.Env = append(os.Environ(), "HOME="+home)
		out, _ := c.CombinedOutput()
		return string(out)
	}
	list := run(pruneListScriptFor(h))
	if !strings.Contains(list, "mirror\t"+MirrorKey("work/busy")+"\tclaimed\twork/busy") || !strings.Contains(list, "\tfree\twork/gone") {
		t.Errorf("listing:\n%s", list)
	}
	if out := run(pruneMirrorScript(h, "work/busy")); !strings.Contains(out, "busy") {
		t.Errorf("a claimed mirror was not refused: %s", out)
	}
	if _, err := os.Stat(filepath.Join(home, h.Root, "work/busy")); err != nil {
		t.Error("a claimed mirror was removed")
	}
	run(pruneMirrorScript(h, "work/gone"))
	for _, p := range []string{filepath.Join(home, h.Root, "work/gone"), filepath.Join(records, MirrorKey("work/gone")), filepath.Join(records, MirrorKey("work/gone")+".claim")} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s survived", p)
		}
	}
}

// A mirror path that crosses a symlink is refused, so prune never deletes
// outside the mirror root.
func TestPruneRefusesASymlinkedMirrorPath(t *testing.T) {
	home := t.TempDir()
	h := testHost()
	outside := filepath.Join(home, "precious")
	if err := os.MkdirAll(filepath.Join(outside, "gone"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, h.Root, ".dot-remote"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(home, h.Root, "work")); err != nil {
		t.Fatal(err)
	}
	c := osexec.Command("bash", "-c", pruneMirrorScript(h, "work/gone"))
	c.Env = append(os.Environ(), "HOME="+home)
	out, _ := c.CombinedOutput()
	if !strings.Contains(string(out), "is a symlink") {
		t.Errorf("not refused: %s", out)
	}
	if _, err := os.Stat(filepath.Join(outside, "gone")); err != nil {
		t.Error("prune deleted through a symlink")
	}
}

// Per-run verify refs and their -base are both listed and removed with the
// verify container of their key checked.
func TestPruneHandlesPerRunVerifyRefs(t *testing.T) {
	root := t.TempDir()
	h := testHost()
	f := &pruneFixture{listing: "ref\tr.git\trefs/verify/k1/abc-n\nref\tr.git\trefs/verify/k1/abc-n-base\n"}
	report, err := f.deps(root).Prune(context.Background(), Config{LocalRoot: root}, h, PruneOptions{Yes: true})
	if err != nil || len(report.Items) != 2 {
		t.Fatalf("%v %+v", err, report.Items)
	}
	joined := strings.Join(f.calls, "\n")
	for _, want := range []string{"'refs/verify/k1/abc-n'", "'refs/verify/k1/abc-n-base'", "name=^me-rdev-verify-k1$"} {
		if !strings.Contains(joined, want) {
			t.Errorf("lacks %s:\n%s", want, joined)
		}
	}
}
