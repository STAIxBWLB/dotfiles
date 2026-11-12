package remote

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/admission"
	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

const statusReply = "mirror\t1024\twork/dev/one\n" +
	"mirror\t2048\twork/dev/two\n" +
	"bare\t512\tone.git\n" +
	"created\t2026-01-01T10:00:00.000000000Z\n"

// statusConfig is the config a status uses to map a lease's local path back to
// the mirror path its container is named after.
func statusConfig(localRoot string) Config {
	return Config{LocalRoot: localRoot, Hosts: map[string]Host{"buildbox": testHost()}}
}

func TestStatusReadsSizesAndTheImageAge(t *testing.T) {
	fake := &fakeExec{t: t, loads: "1.00 0.50 0.25 1/100 42\n8\n", output: statusReply}
	now := time.Date(2026, 1, 3, 10, 0, 0, 0, time.UTC)
	deps := Deps{Exec: fake.execute, Now: func() time.Time { return now }}
	status, err := deps.Status(context.Background(), statusConfig("/home/u/workspace"), testHost())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Host != "buildbox" || status.Image != "ghcr.io/org/dev:1" {
		t.Errorf("status = %+v", status)
	}
	want := []DirSize{{Name: "work/dev/one", Bytes: 1024 * 1024}, {Name: "work/dev/two", Bytes: 2048 * 1024}}
	if !slices.Equal(status.Mirrors, want) {
		t.Errorf("mirrors = %+v, want %+v", status.Mirrors, want)
	}
	if !slices.Equal(status.BareRepos, []DirSize{{Name: "one.git", Bytes: 512 * 1024}}) {
		t.Errorf("bare repos = %+v", status.BareRepos)
	}
	if status.ImageCreated != "2026-01-01T10:00:00.000000000Z" {
		t.Errorf("image created = %q", status.ImageCreated)
	}
	if status.ImageAge != "48h0m0s" {
		t.Errorf("image age = %q, want two days", status.ImageAge)
	}
	if status.SizesUnavailable != "" {
		t.Errorf("sizes unavailable: %s", status.SizesUnavailable)
	}
	if !status.Load.Available() || status.Load.CPUs != 8 {
		t.Errorf("load = %+v", status.Load)
	}
}

// A host that cannot answer still yields the local half: the leases this
// machine holds are facts it does not have to confirm.
func TestStatusKeepsTheLocalLeasesWhenTheHostIsUnreachable(t *testing.T) {
	root := t.TempDir()
	store := admission.NewStore(root, exec.NewProbeRunner())
	self := selfLease(t, root)
	host := testHost()
	path := "/home/u/workspace/one"
	slot, err := AcquirePath(context.Background(), store, self, host, path, 0)
	if err != nil {
		t.Fatalf("holding a lease: %v", err)
	}
	defer slot.Release()

	fake := &fakeExec{t: t, loads: "1.00 0.50 0.25 1/100 42\n8\n", exit: 1}
	deps := Deps{Exec: fake.execute, Store: store}
	status, err := deps.Status(context.Background(), statusConfig("/home/u/workspace"), host)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.SizesUnavailable == "" {
		t.Error("an unreachable host reported sizes")
	}
	if len(status.Mirrors) != 0 || len(status.BareRepos) != 0 {
		t.Errorf("an unreachable host reported sizes: %+v %+v", status.Mirrors, status.BareRepos)
	}
	if len(status.Active) != 1 || status.Active[0].Scope != PathScope(path) {
		t.Fatalf("active runs = %+v, want the lease this machine holds", status.Active)
	}
	if status.Active[0].Kind != "path" || status.Active[0].Container != host.ContainerName("one") {
		t.Errorf("active run = %+v, want a path lease naming its container", status.Active[0])
	}
}

func TestActiveRunsSelectsThisHostsRemoteLeases(t *testing.T) {
	host := testHost()
	now := time.Date(2026, 2, 1, 9, 0, 0, 0, time.UTC)
	leases := []admission.Lease{
		{Class: admission.ClassRemote, Scope: "/home/u/one", Host: host.Name, Owner: "me@mac", PID: 10, AcquiredAt: now, Session: "dot remote"},
		{Class: admission.ClassRemote, Scope: HostScope(host.Name), Host: host.Name, Owner: "me@mac", PID: 11, AcquiredAt: now},
		{Class: admission.ClassRemote, Scope: "/home/u/two", Host: "otherbox", Owner: "me@mac", PID: 12},
		{Class: admission.ClassHeavy, Scope: "/home/u/.git", Host: host.Name, Owner: "me@mac", PID: 13},
	}
	runs := activeRuns(statusConfig("/home/u"), host, leases, now)
	if len(runs) != 2 {
		t.Fatalf("runs = %+v, want only this host's remote leases", runs)
	}
	if runs[0].Kind != "path" || runs[0].Container != host.ContainerName("one") {
		t.Errorf("path lease = %+v", runs[0])
	}
	if runs[0].Session != "dot remote" || runs[0].PID != 10 {
		t.Errorf("path lease = %+v, want the recorded session and pid", runs[0])
	}
	if runs[1].Kind != "host" || runs[1].Container != "" {
		t.Errorf("host lease = %+v, want no container name", runs[1])
	}
	// A lease with no recorded time still shows an age rather than the zero
	// time, which would read as ancient.
	undated := activeRuns(statusConfig("/home/u"), host, []admission.Lease{{Class: admission.ClassRemote, Scope: "/home/u/three", Host: host.Name}}, now)
	if len(undated) != 1 || !undated[0].Since.Equal(now) {
		t.Errorf("undated lease = %+v, want the reference time", undated)
	}
}

// The verdict a person reads is the same sentence a run defers with.
func TestStatusVerdictUsesTheHostsOwnLimit(t *testing.T) {
	host := testHost()
	host.MaxLoadRatio = 0.5
	fake := &fakeExec{t: t, loads: "6.00 3.00 1.00 1/100 42\n8\n", output: statusReply}
	deps := Deps{Exec: fake.execute}
	status, err := deps.Status(context.Background(), statusConfig("/home/u/workspace"), host)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !strings.Contains(status.Verdict, "at or over 0.5 x its CPUs") {
		t.Errorf("verdict = %q", status.Verdict)
	}
}

// A mirror is not a top-level directory: a checkout at local_root/work/dev/one
// is mirrored to <root>/work/dev/one, so the script sizes the paths the runs
// recorded rather than `du -sk *` at the root.
func TestTheStatusScriptSizesTheRecordedMirrors(t *testing.T) {
	fake := &fakeExec{t: t, loads: "1.00 0.50 0.25 1/100 42\n8\n", output: statusReply}
	deps := Deps{Exec: fake.execute}
	if _, err := deps.Status(context.Background(), statusConfig("/home/u/workspace"), testHost()); err != nil {
		t.Fatalf("Status: %v", err)
	}
	script := fake.calls[len(fake.calls)-1][len(fake.calls[len(fake.calls)-1])-1]
	for _, want := range []string{".dot-remote", `du -sk "$HOME"/'dot-remote'/"$rel"`, "$rel"} {
		if !strings.Contains(script, want) {
			t.Errorf("the status script does not carry %q:\n%s", want, script)
		}
	}
	if strings.Contains(script, "du -sk *") {
		t.Errorf("the status script still sizes the root's top-level directories:\n%s", script)
	}
}

// A host that cannot measure a mirror says so instead of reporting an empty
// list, which would read as "nothing there".
func TestASizeFailureIsReportedNotSwallowed(t *testing.T) {
	fake := &fakeExec{t: t, loads: "1.00 0.50 0.25 1/100 42\n8\n",
		output: "error\tcannot measure the mirror of work/dev/one\ncreated\t2026-01-01T10:00:00Z\n"}
	deps := Deps{Exec: fake.execute}
	status, err := deps.Status(context.Background(), statusConfig("/home/u/workspace"), testHost())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !strings.Contains(status.SizesUnavailable, "cannot measure the mirror of work/dev/one") {
		t.Errorf("sizes_unavailable = %q, want the host's own reason", status.SizesUnavailable)
	}
	if len(status.Mirrors) != 0 {
		t.Errorf("mirrors = %+v, want the unmeasurable one left out", status.Mirrors)
	}
}

// The record directory is what makes an empty mirror list mean something, so a
// host that cannot list it - or cannot read one of the records, or reads an
// empty one - has to say so. Silently reporting a clean empty list would read
// as "this host has no mirrors" for a permission problem or a truncated record.
func TestAStatusThatCannotReadTheRecordsSaysWhy(t *testing.T) {
	const reply = "error\tcannot list /home/u/dot-remote/.dot-remote (Permission denied)\n" +
		"error\tcannot read the record of 4f2a (Permission denied)\n" +
		"error\tthe record of 9c1b is empty\n"
	fake := &fakeExec{t: t, loads: "1.00 0.50 0.25 1/100 42\n8\n", output: reply}
	deps := Deps{Exec: fake.execute}
	status, err := deps.Status(context.Background(), statusConfig("/home/u/workspace"), testHost())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	for _, want := range []string{
		"cannot list /home/u/dot-remote/.dot-remote (Permission denied)",
		"cannot read the record of 4f2a (Permission denied)",
		"the record of 9c1b is empty",
	} {
		if !strings.Contains(status.SizesUnavailable, want) {
			t.Errorf("sizes_unavailable does not carry %q: %q", want, status.SizesUnavailable)
		}
	}
	if len(status.Mirrors) != 0 {
		t.Errorf("mirrors = %+v, want nothing sized", status.Mirrors)
	}

	// The reasons come from the script's own arms: each command's status is
	// checked there, because sh would otherwise hand back an empty string that
	// reads as a mirror of size zero.
	script := fake.calls[len(fake.calls)-1][len(fake.calls[len(fake.calls)-1])-1]
	for _, want := range []string{
		`if ! listing=$(ls -A "$records" 2>&1)`,
		`if ! rel=$(cat "$record" 2>&1)`,
		"the record of %s is empty",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("the status script does not carry %q:\n%s", want, script)
		}
	}
}
