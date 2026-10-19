package admission

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func fixtureRepository(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestHeavyweightInventoryClassification(t *testing.T) {
	for _, tc := range []struct {
		name, args string
		want       bool
	}{{"cargo", "cargo test --lib", true}, {"go", "go test ./...", true}, {"cp", "cp -cR /x/target /y/target", true}, {"node", "node /x/playwright test", true}, {"go", "go version", false}, {"ps", "ps -axo pid", false}, {"git", "git status", false}, {"node", "node server.js", false}} {
		if got := heavyweight(tc.name, tc.args); got != tc.want {
			t.Errorf("%s %s: got %t", tc.name, tc.args, got)
		}
	}
}

func TestRepositoryIdentityCollapsesWorktrees(t *testing.T) {
	repo := fixtureRepository(t)
	other := fixtureRepository(t)
	worktree := t.TempDir()
	metadata := filepath.Join(repo, ".git", "worktrees", "linked")
	if err := os.MkdirAll(metadata, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(metadata, "commondir"), []byte("../..\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: "+metadata+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	main, err := repositoryIdentity(repo)
	if err != nil {
		t.Fatal(err)
	}
	linked, err := repositoryIdentity(worktree)
	if err != nil || linked != main {
		t.Fatalf("main=%q linked=%q err=%v", main, linked, err)
	}
	if o, _ := repositoryIdentity(other); o == main {
		t.Fatal("different repositories share an identity")
	}
	if _, err := repositoryIdentity(t.TempDir()); err == nil {
		t.Fatal("a non-repository resolved to an identity")
	}
}

func TestUncoveredJobsAreScopedButUnknownBlocks(t *testing.T) {
	for _, tc := range []struct {
		repo string
		want int
	}{{"/a/.git", 1}, {"/b/.git", 0}, {"", 1}} {
		if got := filterUncovered([]HeavyJob{{PID: 12, Repository: tc.repo}}, "/a/.git", false); len(got) != tc.want {
			t.Fatalf("repo=%q blockers=%v", tc.repo, got)
		}
	}
	if got := filterUncovered([]HeavyJob{{Repository: "/b/.git", Maintenance: true}}, "", true); len(got) != 1 {
		t.Fatal("shared maintenance collision missed")
	}
	if got := filterUncovered([]HeavyJob{{Repository: "/b/.git"}}, "", true); len(got) != 0 {
		t.Fatalf("non-maintenance work in another repo blocked the maintenance slot: %v", got)
	}
}

func TestFailedLsofBatchDiscardsPartialOwnership(t *testing.T) {
	dirs := lsofDirectories("p123\nn/other/repository\np456\n", context.DeadlineExceeded)
	if len(dirs) != 0 {
		t.Fatalf("trusted partial failed telemetry: %v", dirs)
	}
	job := HeavyJob{PID: 123, Directory: dirs[123]}
	if got := filterUncovered([]HeavyJob{job}, "/current/.git", false); len(got) != 1 {
		t.Fatal("uncertain ownership failed open")
	}
	if complete := lsofDirectories("p123\nn/other/repository\n", nil); complete[123] != "/other/repository" {
		t.Fatal("complete telemetry rejected")
	}
}

func TestUserStateRootFollowsHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	got, err := UserStateRoot()
	if err != nil || got != DefaultStateRoot(home) {
		t.Fatalf("UserStateRoot = %q, %v; want %q", got, err, DefaultStateRoot(home))
	}
}

// TestParseProcessTableNamesFromArgv0: tools started by absolute path are
// classified by their real name, not a truncated comm column.
func TestParseProcessTableNamesFromArgv0(t *testing.T) {
	rows, parents, err := parseProcessTable("  101     1   5.0 /opt/homebrew/bin/go test ./...\n  102   101  99.0 /usr/bin/make -j2\n  103     1   0.1 /opt/homebrew/bin/dot ai memory bridge\n")
	if err != nil || len(rows) != 3 || parents[102] != 101 {
		t.Fatalf("rows=%+v parents=%v err=%v", rows, parents, err)
	}
	for i, want := range []bool{true, true, false} {
		if got := heavyweight(rows[i].name, rows[i].args); got != want {
			t.Errorf("row %+v heavyweight = %v, want %v", rows[i], got, want)
		}
	}
}

// Heavy work inside a leased process tree is owned by that slot; only work
// no owner accounts for is uncovered.
func TestUnownedHeavyJobsSkipsLeasedTrees(t *testing.T) {
	rows, parents, err := parseProcessTable("" +
		"  100     1   0.5 /opt/homebrew/bin/dot ai update\n" + // leased supervisor
		"  101   100  50.0 /usr/local/bin/npm install -g x\n" + // its child: owned
		"  102   101  90.0 /usr/local/bin/node install.js install\n" + // grandchild: owned
		"  200     1  40.0 /opt/homebrew/bin/go test ./...\n" + // nobody's: uncovered
		"  300     1   1.0 /bin/zsh\n" + // self's parent
		"  301   300  80.0 /usr/bin/make -j2\n") // self's own child
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := unownedHeavyJobs(rows, parents, 300, []int{100})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].PID != 200 {
		t.Fatalf("unowned jobs = %+v, want only pid 200", jobs)
	}
	if all, _ := unownedHeavyJobs(rows, parents, 300, nil); len(all) != 3 {
		t.Fatalf("without leases = %+v, want pids 101, 102 and 200", all)
	}
}

// FindUncovered hands the leased PIDs through to the process-table scan.
// The fake PIDs exist nowhere, so every job's repository is unknown.
func TestFindUncoveredExcludesLeasedTrees(t *testing.T) {
	orig := processTable
	t.Cleanup(func() { processTable = orig })
	processTable = func(context.Context) (string, error) {
		return "" +
			"2000000100          1   0.5 /opt/homebrew/bin/dot ai update\n" +
			"2000000101 2000000100  50.0 /usr/local/bin/npm install -g x\n" +
			"2000000200          1  40.0 /opt/homebrew/bin/go test ./...\n", nil
	}
	got, err := FindUncovered(context.Background(), fixtureRepository(t), false, []int{2000000100})
	if err != nil || len(got) != 1 || !strings.Contains(got[0], "pid=2000000200") {
		t.Fatalf("uncovered = %v, %v; want only pid 2000000200", got, err)
	}
}

// An expired lease no longer vouches for its PID's process tree.
func TestLeasedPIDsSkipsExpiredLeases(t *testing.T) {
	now := time.Date(2026, 9, 28, 3, 0, 0, 0, time.UTC)
	store := NewStore(t.TempDir(), nil)
	store.Now = func() time.Time { return now }
	seed := func(scope string, pid int) {
		slot, _, err := store.Acquire(context.Background(), scope, ClassHeavy, Lease{Owner: "t@mac", PID: pid, PIDStart: "x"})
		if err != nil || slot == nil {
			t.Fatalf("seeding %s = %v, %v", scope, slot, err)
		}
	}
	seed("repo-old", 11)
	now = now.Add(2 * store.HeartbeatStaleAfter())
	seed("repo-new", 22)
	if got := LeasedPIDs(store); len(got) != 1 || got[0] != 22 {
		t.Fatalf("LeasedPIDs = %v, want only the live lease 22", got)
	}
}

// dot's own scheduled peer sync runs from launchd with cwd "/", so its rsync
// child has no repository and used to defer every repo and the maintenance
// slot (#267). Under its background lease the whole tree is covered.
func TestBackgroundSyncLeaseCoversItsRsyncChild(t *testing.T) {
	const self, dot, rsync = 9000, 4100, 4101
	table := "4100 1 0.0 /opt/homebrew/bin/dot peer sync\n" +
		"4101 4100 100.0 /opt/homebrew/bin/rsync -a --delete /Users/u/work/ peer:/Users/u/work/\n"
	rows, parents, err := parseProcessTable(table)
	if err != nil {
		t.Fatal(err)
	}

	store := NewStore(t.TempDir(), nil)
	slot, _, err := store.Acquire(context.Background(), "peer sync", ClassBackground, Lease{Owner: "dot@test", PID: dot, PIDStart: "Sat Oct 18 04:00:00 2026"})
	if err != nil || slot == nil {
		t.Fatalf("background lease = %v, %v", slot, err)
	}
	for _, leased := range [][]int{nil, LeasedPIDs(store)} {
		jobs, err := unownedHeavyJobs(rows, parents, self, leased)
		if err != nil {
			t.Fatal(err)
		}
		for _, maintenance := range []bool{false, true} {
			blocking := filterUncovered(jobs, "/Users/u/other/.git", maintenance)
			if leased == nil && len(blocking) == 0 {
				t.Fatal("fixture is vacuous: without the lease the rsync child must block")
			}
			if leased != nil && len(blocking) != 0 {
				t.Fatalf("maintenance=%v: the leased sync's rsync still blocks: %v", maintenance, blocking)
			}
		}
	}
}

// A busy process with no build or test shape, whose cwd is known and lies
// outside every repository, is host load: it never blocks as unknown ownership
// (#294). Everything else that is unknown still blocks.
func TestCPUOnlyWorkOutsideRepositoriesDoesNotBlock(t *testing.T) {
	for _, tc := range []struct {
		name        string
		job         HeavyJob
		maintenance bool
		want        int
	}{
		{"busy app process outside any repository", HeavyJob{PID: 1, CPUOnly: true, OutsideRepository: true}, false, 0},
		{"busy process with unobserved cwd", HeavyJob{PID: 2, CPUOnly: true}, false, 1},
		{"build-shaped work outside any repository", HeavyJob{PID: 3, OutsideRepository: true}, false, 1},
		{"maintenance-shaped busy process outside any repository", HeavyJob{PID: 4, CPUOnly: true, OutsideRepository: true, Maintenance: true}, true, 1},
		{"busy process outside any repository, maintenance class", HeavyJob{PID: 5, CPUOnly: true, OutsideRepository: true}, true, 0},
	} {
		if got := filterUncovered([]HeavyJob{tc.job}, "/a/.git", tc.maintenance); len(got) != tc.want {
			t.Errorf("%s: blockers=%v, want %d", tc.name, got, tc.want)
		}
	}
}

func TestRepositoryIdentityReportsNotRepository(t *testing.T) {
	if _, err := repositoryIdentity(tempOutsideRepositories(t)); !errors.Is(err, errNotRepository) {
		t.Fatalf("plain directory: err=%v, want errNotRepository", err)
	}
	broken := t.TempDir()
	if err := os.WriteFile(filepath.Join(broken, ".git"), []byte("not a pointer\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := repositoryIdentity(broken); err == nil || errors.Is(err, errNotRepository) {
		t.Fatalf("invalid Git pointer: err=%v, want an unknown-ownership error", err)
	}
}

// tempOutsideRepositories returns a temporary directory, or skips when the
// host's TMPDIR itself lies inside a repository (some agent sandboxes).
func tempOutsideRepositories(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if _, err := repositoryIdentity(dir); !errors.Is(err, errNotRepository) {
		t.Skipf("TMPDIR %s is not outside every repository: %v", dir, err)
	}
	return dir
}

// spawnSleeper starts a child whose cwd is dir and stops it at cleanup.
func spawnSleeper(t *testing.T, dir string) int {
	t.Helper()
	c := exec.Command("/bin/sleep", "30")
	c.Dir = dir
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Process.Kill(); _ = c.Wait() })
	return c.Process.Pid
}

func TestResolveJobDirectoriesMarksOutsideRepository(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("cwd resolution is implemented for linux and darwin")
	}
	outside := spawnSleeper(t, tempOutsideRepositories(t))
	gone := t.TempDir()
	removed := spawnSleeper(t, gone)
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	jobs := resolveJobDirectories(context.Background(), []HeavyJob{{PID: os.Getpid()}, {PID: outside}, {PID: removed}})
	if jobs[0].Repository == "" || jobs[0].OutsideRepository {
		t.Errorf("own process (in this repository): %+v, want a repository", jobs[0])
	}
	if !jobs[1].OutsideRepository || jobs[1].Repository != "" {
		t.Errorf("child in a plain temporary directory: %+v, want OutsideRepository", jobs[1])
	}
	if jobs[2].OutsideRepository {
		t.Errorf("child whose cwd was removed: %+v, want unknown ownership, not outside", jobs[2])
	}
}

func TestRepositoryIdentityMissingPathIsUnknown(t *testing.T) {
	if _, err := repositoryIdentity(filepath.Join(t.TempDir(), "missing")); err == nil || errors.Is(err, errNotRepository) {
		t.Fatalf("nonexistent path: err=%v, want an unknown-ownership error", err)
	}
}

// A caller outside every repository has project "": an exempted job must not
// match it as "the same repository" (#294 D5).
func TestExemptJobDoesNotMatchAnEmptyProject(t *testing.T) {
	exempt := HeavyJob{PID: 7, CPUOnly: true, OutsideRepository: true}
	if got := filterUncovered([]HeavyJob{exempt}, "", false); len(got) != 0 {
		t.Fatalf("caller outside every repository blocked on exempt work: %v", got)
	}
	if got := filterUncovered([]HeavyJob{{PID: 8, CPUOnly: true}}, "", false); len(got) != 1 {
		t.Fatalf("unknown ownership no longer blocks a caller outside every repository: %v", got)
	}
}

func TestUnownedHeavyJobsMarksCPUOnly(t *testing.T) {
	rows, parents, err := parseProcessTable("" +
		"  400     1 150.0 /Users/x/.local/bin/claude --max-turns=1 --model=fast\n" +
		"  401     1 150.0 /opt/homebrew/bin/cargo build --release\n")
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := unownedHeavyJobs(rows, parents, 999, nil)
	if err != nil || len(jobs) != 2 {
		t.Fatalf("jobs=%+v err=%v", jobs, err)
	}
	if !jobs[0].CPUOnly || jobs[1].CPUOnly {
		t.Fatalf("CPUOnly: claude=%t cargo=%t, want true and false", jobs[0].CPUOnly, jobs[1].CPUOnly)
	}
}
