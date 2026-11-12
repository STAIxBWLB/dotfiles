package remote

import (
	"os"
	osexec "os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The claim, its takeover check and its release run as shell on the host, so
// what they do about a claim that changed hands, a directory that cannot be
// created and a removal that fails is only visible by running that shell. These
// tests drive the script text this package sends - the same builders a run uses -
// in a sandbox $HOME, and read what the host would have printed.

// claimSandbox is a fake remote $HOME: one mirror's record directory, one
// checkout path, and a runtime stub good enough for the inspection script.
type claimSandbox struct {
	t     *testing.T
	home  string
	host  Host
	rel   string
	path  string
	deps  Deps
	admin string
}

// claimOwner is the record the sandbox's own machine writes: machine, pid,
// start, checkout.
const (
	claimMachine = "maker"
	claimStarted = "2026-01-01T00:00:00Z"
)

func newClaimSandbox(t *testing.T) *claimSandbox {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	checkout := filepath.Join(home, "work", "one")
	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(checkout, 0o755); err != nil {
		t.Fatal(err)
	}
	// The inspection script asks the runtime two questions and the image a
	// third; the answers do not matter here, only that the claim arm is reached.
	stub := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"  ps) [ -n \"$STUB_CONTAINER\" ] && printf '%s\\n' \"$STUB_CONTAINER\" ;;\n" +
		"  image) printf '1000:1000\\n' ;;\n" +
		"esac\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	host := testHost()
	host.Root = DefaultRoot
	host.Runtime = "docker"
	host.Ref = "ghcr.io/org/dev:1"
	// The inspection creates the record directory (`mkdir -p`) before the take
	// runs, so the sandbox starts the way a run's host does.
	if err := os.MkdirAll(filepath.Join(home, host.Root, ".dot-remote"), 0o755); err != nil {
		t.Fatal(err)
	}
	return &claimSandbox{
		t: t, home: home, host: host, rel: "one", path: checkout, admin: bin,
		deps: Deps{Machine: claimMachine, Started: claimStarted},
	}
}

// records is the record directory this host keeps its claims in.
func (s *claimSandbox) records() string {
	return filepath.Join(s.home, s.host.Root, ".dot-remote")
}

// claimDir is where the scripts put this mirror's claim.
func (s *claimSandbox) claimDir() string {
	return filepath.Join(s.records(), MirrorKey(s.rel)+".claim")
}

// ownerRecord is the record a claim of this machine and this checkout carries.
func (s *claimSandbox) ownerRecord() string {
	return claimMachine + "\t4242\t" + claimStarted + "\t" + s.path
}

// plant writes a claim whose owner record is the argument, parents included.
func (s *claimSandbox) plant(record string) {
	s.t.Helper()
	if err := os.MkdirAll(s.claimDir(), 0o755); err != nil {
		s.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.claimDir(), "owner"), []byte(record), 0o644); err != nil {
		s.t.Fatal(err)
	}
}

// owner reads the claim's owner record back, empty when the claim is gone.
func (s *claimSandbox) owner() string {
	s.t.Helper()
	data, err := os.ReadFile(filepath.Join(s.claimDir(), "owner"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// exists reports whether the claim directory is still there.
func (s *claimSandbox) exists() bool {
	info, err := os.Stat(s.claimDir())
	return err == nil && info.IsDir()
}

// run executes one script the way ssh would: /bin/sh with this sandbox as the
// host's $HOME, and only the stub runtime on PATH.
func (s *claimSandbox) run(script string) string {
	s.t.Helper()
	cmd := osexec.Command("/bin/sh", "-c", script)
	cmd.Env = []string{"HOME=" + s.home, "PATH=" + s.admin + ":/usr/bin:/bin", "LC_ALL=C"}
	out, err := cmd.Output()
	if err != nil {
		s.t.Fatalf("running the host script failed (%v): %s", err, out)
	}
	return string(out)
}

// seal makes the record directory unwritable, so the scripts inside it fail the
// way a permission problem on the host fails.
func (s *claimSandbox) seal() {
	s.t.Helper()
	if err := os.MkdirAll(s.records(), 0o755); err != nil {
		s.t.Fatal(err)
	}
	if err := os.Chmod(s.records(), 0o500); err != nil {
		s.t.Fatal(err)
	}
	s.t.Cleanup(func() { _ = os.Chmod(s.records(), 0o755) })
}

// The take is one mkdir under the record directory, and the owner record it
// leaves is what a later run reads to tell its own leftover from another run's:
// machine, pid, start and the checkout it belongs to.
func TestTheClaimScriptTakesAFreeMirrorAndRecordsItsOwner(t *testing.T) {
	s := newClaimSandbox(t)
	out := s.run(claimScript(s.host, s.rel, s.path, s.deps, false))
	if strings.TrimSpace(out) != "claim\tacquired" {
		t.Fatalf("the take printed %q, want an acquired claim", out)
	}
	record := s.owner()
	fields := strings.Split(record, "\t")
	if len(fields) != 4 {
		t.Fatalf("owner record %q has %d fields, want machine, pid, start and checkout", record, len(fields))
	}
	if fields[0] != claimMachine || fields[2] != claimStarted || fields[3] != s.path {
		t.Errorf("owner record = %q, want %s, a pid, %s and %s", record, claimMachine, claimStarted, s.path)
	}
	if !regexp.MustCompile(`^[0-9]+$`).MatchString(fields[1]) {
		t.Errorf("owner record pid = %q, want a number", fields[1])
	}
}

// mkdir loses only when someone else got the directory first. Every other
// failure is reported as the failure it is, with the reason the shell printed.
func TestTheClaimScriptReportsWhyItCouldNotCreateTheClaim(t *testing.T) {
	s := newClaimSandbox(t)
	s.seal()
	out := s.run(claimScript(s.host, s.rel, s.path, s.deps, false))
	if !strings.Contains(out, "claim\tfailed\tcannot create the claim under") {
		t.Fatalf("the take printed %q, want a failure naming the mkdir", out)
	}
	if strings.Contains(out, "lost-race") {
		t.Errorf("a permission failure was reported as losing the race: %q", out)
	}
	if !strings.Contains(out, "Permission denied") {
		t.Errorf("the failure does not carry the shell's own reason: %q", out)
	}
	if s.exists() {
		t.Error("a failed take left a claim behind")
	}
}

// With a claim already there the mkdir loses, and the run defers rather than
// reporting a failure: that is the race the atomic take is for.
func TestTheClaimScriptLosesTheRaceWhenTheClaimIsAlreadyThere(t *testing.T) {
	s := newClaimSandbox(t)
	s.plant("another-run\t99\t2026-01-01T00:00:00Z\t/elsewhere\n")
	out := s.run(claimScript(s.host, s.rel, s.path, s.deps, false))
	if strings.TrimSpace(out) != "claim\tlost-race" {
		t.Fatalf("the take printed %q, want a lost race", out)
	}
	if got := s.owner(); !strings.HasPrefix(got, "another-run\t") {
		t.Errorf("the take overwrote the other run's record: %q", got)
	}
}

// A takeover is only this run's when the record still names this machine and
// this checkout. Anything else defers, because the local path lease proves one
// previous run dead and the record is what says which one.
func TestTheClaimScriptOnlyReplacesItsOwnLeftover(t *testing.T) {
	cases := map[string]struct {
		record string
		want   string
	}{
		"another machine":                  {"otherbox\t4242\t" + claimStarted + "\t/path\n", "claim\tforeign\totherbox"},
		"another checkout of this machine": {claimMachine + "\t4242\t" + claimStarted + "\t/other/clone\n", "claim\tforeign\t" + claimMachine},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			s := newClaimSandbox(t)
			s.plant(tc.record)
			out := s.run(claimScript(s.host, s.rel, s.path, s.deps, true))
			if !strings.HasPrefix(strings.TrimSpace(out), tc.want) {
				t.Fatalf("the takeover printed %q, want %q", out, tc.want)
			}
			if !s.exists() {
				t.Error("a takeover that deferred removed the claim it did not own")
			}
			if got := s.owner(); got != strings.TrimSpace(tc.record) {
				t.Errorf("the claim's record changed to %q", got)
			}
		})
	}
}

// The leftover this run may take is replaced, and the record it leaves is this
// run's: same machine and checkout, a new pid and start.
func TestTheClaimScriptReplacesItsOwnLeftover(t *testing.T) {
	s := newClaimSandbox(t)
	s.plant(claimMachine + "\t4242\t2019-05-05T00:00:00Z\t" + s.path + "\n")
	out := s.run(claimScript(s.host, s.rel, s.path, s.deps, true))
	if strings.TrimSpace(out) != "claim\tacquired" {
		t.Fatalf("the takeover printed %q, want an acquired claim", out)
	}
	fields := strings.Split(s.owner(), "\t")
	if len(fields) != 4 || fields[0] != claimMachine || fields[2] != claimStarted || fields[3] != s.path {
		t.Fatalf("owner record = %q, want this run's", s.owner())
	}
	if fields[1] == "4242" {
		t.Errorf("owner record still carries the dead run's pid: %q", fields[1])
	}
}

// A leftover that cannot be removed stops the takeover with the reason and the
// path. Proceeding would mean two runs in one mirror, and reporting a lost race
// would send the person looking for a machine that is not there.
func TestTheClaimScriptReportsAFailedRemovalOfItsOwnLeftover(t *testing.T) {
	s := newClaimSandbox(t)
	s.plant(claimMachine + "\t4242\t" + claimStarted + "\t" + s.path + "\n")
	if err := os.Chmod(s.records(), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(s.records(), 0o755) })
	out := s.run(claimScript(s.host, s.rel, s.path, s.deps, true))
	if !strings.Contains(out, "claim\tfailed\tthe leftover claim under") {
		t.Fatalf("the takeover printed %q, want a failure naming the removal", out)
	}
	if !strings.Contains(out, "could not be removed") || !strings.Contains(out, "Permission denied") {
		t.Errorf("the failure does not carry the reason and what failed: %q", out)
	}
	if strings.Contains(out, "lost-race") || strings.Contains(out, "acquired") {
		t.Errorf("a failed removal was reported as a race or a success: %q", out)
	}
	if !s.exists() {
		t.Error("a failed removal removed the claim anyway")
	}
}

// The inspection answers with the state the run decides on, and "ours" means
// this machine AND this checkout: the record's fourth field is what keeps a
// second clone of this machine from being taken over as a dead predecessor.
func TestTheInspectScriptReadsTheClaimRecord(t *testing.T) {
	cases := map[string]struct {
		record string
		want   string
	}{
		"free":              {"", "claim\tfree"},
		"ours":              {claimMachine + "\t4242\t" + claimStarted + "\tCHECKOUT\n", "claim\tours"},
		"another machine":   {"otherbox\t4242\t" + claimStarted + "\tCHECKOUT\n", "claim\tforeign\totherbox"},
		"another checkout":  {claimMachine + "\t4242\t" + claimStarted + "\t/other/clone\n", "claim\tforeign\t" + claimMachine},
		"three-field owner": {claimMachine + "\t4242\t" + claimStarted + "\n", "claim\tforeign"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			s := newClaimSandbox(t)
			if tc.record != "" {
				s.plant(strings.ReplaceAll(tc.record, "CHECKOUT", s.path))
			}
			out := s.run(inspectScript(s.host, s.rel, s.path, s.claimContainer(), s.deps))
			if !strings.Contains(out, tc.want) {
				t.Fatalf("the inspection printed %q, want it to carry %q", out, tc.want)
			}
		})
	}
}

// An owner file that cannot be read is neither free nor another run's: the run
// has to say it cannot tell instead of guessing.
func TestTheInspectScriptReportsAnUnreadableClaim(t *testing.T) {
	s := newClaimSandbox(t)
	s.plant(s.ownerRecord() + "\n")
	if err := os.Chmod(filepath.Join(s.claimDir(), "owner"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(s.claimDir(), "owner"), 0o644) })
	out := s.run(inspectScript(s.host, s.rel, s.path, s.claimContainer(), s.deps))
	if !strings.Contains(out, "claim\tunreadable") {
		t.Fatalf("the inspection printed %q, want an unreadable claim", out)
	}
}

// The release is what makes a leftover mean a dead run, so it drops exactly the
// claim this run holds and nothing else.
func TestTheReleaseScriptOnlyDropsItsOwnClaim(t *testing.T) {
	cases := map[string]struct {
		record string
		want   bool
	}{
		"its own claim":                  {claimMachine + "\t4242\t" + claimStarted + "\tCHECKOUT\n", true},
		"another machine's claim":        {"otherbox\t4242\t" + claimStarted + "\tCHECKOUT\n", false},
		"this machine, another checkout": {claimMachine + "\t4242\t" + claimStarted + "\t/other/clone\n", false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			s := newClaimSandbox(t)
			s.plant(strings.ReplaceAll(tc.record, "CHECKOUT", s.path))
			out := s.run(releaseScript(s.host, s.rel, s.path, s.deps))
			if strings.TrimSpace(out) != "" {
				t.Errorf("the release printed %q, want nothing", out)
			}
			if gone := !s.exists(); gone != tc.want {
				t.Errorf("claim removed = %v, want %v (%s)", gone, tc.want, name)
			}
		})
	}
}

// A release that cannot remove the claim says so with the reason, instead of
// exiting quietly and leaving the next run to take over a claim it thinks is
// gone.
func TestTheReleaseScriptReportsAFailedRemoval(t *testing.T) {
	s := newClaimSandbox(t)
	s.plant(s.ownerRecord() + "\n")
	if err := os.Chmod(s.records(), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(s.records(), 0o755) })
	out := s.run(releaseScript(s.host, s.rel, s.path, s.deps))
	if !strings.Contains(out, "release\tfailed\tthe claim under") {
		t.Fatalf("the release printed %q, want a failure naming the removal", out)
	}
	if !strings.Contains(out, "Permission denied") {
		t.Errorf("the failure does not carry the shell's own reason: %q", out)
	}
	if !s.exists() {
		t.Error("the claim is gone despite the reported failure")
	}
}

// A release whose rm fails can take the owner file and leave the directory, and
// the next run has to read that as a claim it cannot read - it defers, it does
// not take the claim over. That is why the release warning names the claim and
// the commands that clear it instead of promising a takeover.
func TestAClaimAPartialRemovalLeftBehindIsUnreadableToTheNextRun(t *testing.T) {
	s := newClaimSandbox(t)
	s.plant(s.ownerRecord() + "\n")
	if err := os.Chmod(s.records(), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(s.records(), 0o755) })
	if out := s.run(releaseScript(s.host, s.rel, s.path, s.deps)); !strings.Contains(out, "release\tfailed") {
		t.Fatalf("the release printed %q, want a reported failure", out)
	}
	if err := os.Chmod(s.records(), 0o755); err != nil {
		t.Fatal(err)
	}
	// What a failed rm -rf leaves: it empties the directory, then cannot unlink
	// it because the record directory above it is not writable.
	if !s.exists() {
		t.Fatal("the claim directory is gone, so this is not the partial removal the test is about")
	}
	if got := s.owner(); got != "" {
		t.Fatalf("owner = %q, want the owner file gone and the directory left", got)
	}
	out := s.run(inspectScript(s.host, s.rel, s.path, s.claimContainer(), s.deps))
	if !strings.Contains(out, "claim\tunreadable") {
		t.Fatalf("the inspection printed %q, want the claim read as unreadable", out)
	}
	if strings.Contains(out, "claim\tours") {
		t.Errorf("the inspection read a partially removed claim as this run's own leftover: %q", out)
	}
	// The takeover path agrees: it will not adopt a claim whose record it cannot
	// read, so the leftover needs a person.
	if out := s.run(claimScript(s.host, s.rel, s.path, s.deps, true)); strings.Contains(out, "claim\tacquired") {
		t.Errorf("the takeover adopted a claim with no record: %q", out)
	}
	if !s.exists() {
		t.Error("the takeover removed a claim it could not read")
	}
}

// claimContainer is the name the run derives for this sandbox's mirror, so the
// inspection asks about the container it would use.
func (s *claimSandbox) claimContainer() string { return s.host.ContainerName(s.rel) }
