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

func TestStatusReadsSizesAndTheImageAge(t *testing.T) {
	fake := &fakeExec{t: t, loads: "1.00 0.50 0.25 1/100 42\n8\n", output: statusReply}
	now := time.Date(2026, 1, 3, 10, 0, 0, 0, time.UTC)
	deps := Deps{Exec: fake.execute, Now: func() time.Time { return now }}
	status, err := deps.Status(context.Background(), testHost())
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
	slot, err := AcquirePath(context.Background(), store, self, host, path, 0, false)
	if err != nil {
		t.Fatalf("holding a lease: %v", err)
	}
	defer slot.Release()

	fake := &fakeExec{t: t, loads: "1.00 0.50 0.25 1/100 42\n8\n", exit: 1}
	deps := Deps{Exec: fake.execute, Store: store}
	status, err := deps.Status(context.Background(), host)
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
	if status.Active[0].Kind != "path" || status.Active[0].Container != host.ContainerName(PathKey(path)) {
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
	runs := activeRuns(host, leases, now)
	if len(runs) != 2 {
		t.Fatalf("runs = %+v, want only this host's remote leases", runs)
	}
	if runs[0].Kind != "path" || runs[0].Container != host.ContainerName(PathKey("/home/u/one")) {
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
	undated := activeRuns(host, []admission.Lease{{Class: admission.ClassRemote, Scope: "/home/u/three", Host: host.Name}}, now)
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
	status, err := deps.Status(context.Background(), host)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !strings.Contains(status.Verdict, "at or over 0.5 x its CPUs") {
		t.Errorf("verdict = %q", status.Verdict)
	}
}
