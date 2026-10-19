package admission

// Uncovered heavy work: builds, tests, installs and bulk copies that run
// without holding a slot. The controller cannot see them through its leases,
// so a bounded process-table scan attributes each to a repository and defers
// when one runs in the caller's repository, or when its ownership is unknown
// (moved from internal/resourceguard, #162).

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// HeavyJob is one heavyweight process found outside admission.
type HeavyJob struct {
	PID         int    `json:"pid"`
	ParentPID   int    `json:"parent_pid"`
	Name        string `json:"name"`
	Directory   string `json:"directory,omitempty"`
	Repository  string `json:"repository,omitempty"`
	Maintenance bool   `json:"maintenance,omitempty"`
	// CPUOnly: only the CPU rule matched, not a build, test or install shape.
	CPUOnly bool `json:"cpu_only,omitempty"`
	// OutsideRepository: the cwd was observed and lies outside every repository.
	OutsideRepository bool `json:"outside_repository,omitempty"`
}

// Describe renders a job for a defer reason.
func (j HeavyJob) Describe() string {
	return fmt.Sprintf("%s(pid=%d,parent=%d,repo=%s)", j.Name, j.PID, j.ParentPID, j.Repository)
}

// FindUncovered lists heavy work that blocks a slot for projectDir: work in
// the same repository, maintenance-shaped work for the maintenance class,
// and any work whose repository is unknown. The caller's own ancestors and
// direct children, and the process trees of the leased PIDs (jobs that hold a
// slot), are never counted. A failed scan is an error; callers
// defer on it rather than assume the host is quiet.
func FindUncovered(ctx context.Context, projectDir string, maintenance bool, leased []int) ([]string, error) {
	jobs, err := scanHeavyJobs(ctx, leased)
	if err != nil {
		return nil, err
	}
	project := ""
	if !maintenance {
		project, _ = repositoryIdentity(projectDir)
	}
	return filterUncovered(jobs, project, maintenance), nil
}

func filterUncovered(jobs []HeavyJob, project string, maintenance bool) []string {
	var out []string
	for _, j := range jobs {
		// A busy process with no heavy shape, in no repository, is host load that the
		// pressure thresholds already cover, not work of unknown ownership (#294).
		unknown := j.Repository == "" && !(j.CPUOnly && j.OutsideRepository)
		if unknown || (!maintenance && j.Repository == project) || (maintenance && j.Maintenance) {
			out = append(out, j.Describe())
		}
	}
	return out
}

// cappedBuffer bounds probe output even if a provider emits more than asked.
type cappedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.limit {
		return 0, fmt.Errorf("probe output exceeds %d bytes", b.limit)
	}
	return b.Buffer.Write(p)
}

func boundedOutput(ctx context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// Callers pass only the constants /bin/ps and /usr/sbin/lsof, with fixed
	// flags and integer PIDs; no user input reaches the command.
	c := osexec.CommandContext(ctx, name, args...) // nosemgrep: go.lang.security.audit.dangerous-exec-command
	var b cappedBuffer
	b.limit = 2 << 20
	c.Stdout = &b
	c.Stderr = &b
	err := c.Run()
	return b.String(), err
}

type processRow struct {
	pid, ppid  int
	name, args string
	cpu        float64
}

// processTable is the scan's `ps` read; tests replace it.
var processTable = func(ctx context.Context) (string, error) {
	return boundedOutput(ctx, "/bin/ps", "-axo", "pid=,ppid=,%cpu=,args=")
}

func scanHeavyJobs(ctx context.Context, leased []int) ([]HeavyJob, error) {
	out, err := processTable(ctx)
	if err != nil {
		return nil, fmt.Errorf("process inventory: %w", err)
	}
	rows, parents, err := parseProcessTable(out)
	if err != nil {
		return nil, err
	}
	found, err := unownedHeavyJobs(rows, parents, os.Getpid(), leased)
	if err != nil {
		return nil, err
	}
	return resolveJobDirectories(ctx, found), nil
}

// unownedHeavyJobs picks the heavyweight rows that no admission owner
// accounts for: not self's ancestors or direct children, and not inside the
// process tree of a leased PID.
func unownedHeavyJobs(rows []processRow, parents map[int]int, self int, leased []int) ([]HeavyJob, error) {
	ancestors := map[int]bool{}
	for p := self; p > 0 && !ancestors[p]; p = parents[p] {
		ancestors[p] = true
	}
	owned := map[int]bool{}
	for _, pid := range leased {
		if pid > 0 {
			owned[pid] = true
		}
	}
	underLease := func(pid int) bool {
		for p, depth := pid, 0; p > 0 && depth < 64; p, depth = parents[p], depth+1 {
			if owned[p] {
				return true
			}
		}
		return false
	}
	var found []HeavyJob
	for _, r := range rows {
		if ancestors[r.pid] || r.ppid == self || underLease(r.pid) {
			continue
		}
		shaped := heavyweight(r.name, r.args)
		if shaped || r.cpu >= 100 {
			found = append(found, HeavyJob{PID: r.pid, ParentPID: r.ppid, Name: r.name, CPUOnly: !shaped, Maintenance: containsWord(r.args, "install", "update", "upgrade", "sync", "ci")})
			if len(found) > 20 {
				return nil, fmt.Errorf("heavy process inventory exceeds 20 jobs; ownership cannot be established safely")
			}
		}
	}
	return found, nil
}

// LeasedPIDs lists the PIDs of the store's live leases, for FindUncovered.
// A lease past its deadline is left out: its holder may be gone and its PID
// reused, and treating that PID's tree as owned would hide uncovered work. A
// read error excludes nothing, which errs toward deferring.
func LeasedPIDs(store *Store) []int {
	leases, err := store.ListLeases()
	if err != nil {
		return nil
	}
	now := store.now()
	pids := make([]int, 0, len(leases))
	for _, l := range leases {
		if now.Before(l.Deadline) {
			pids = append(pids, l.PID)
		}
	}
	return pids
}

// parseProcessTable reads `ps -axo pid=,ppid=,%cpu=,args=` output. The name
// is argv[0]'s base name: macOS truncates the comm column to 16 characters,
// so /opt/homebrew/bin/go would read as "bi" and escape the name rules.
func parseProcessTable(out string) ([]processRow, map[int]int, error) {
	var rows []processRow
	parents := map[int]int{}
	scan := bufio.NewScanner(strings.NewReader(out))
	scan.Buffer(make([]byte, 4096), 1<<20)
	for scan.Scan() {
		f := strings.Fields(scan.Text())
		if len(f) < 4 {
			continue
		}
		pid, e := strconv.Atoi(f[0])
		if e != nil {
			continue
		}
		ppid, e := strconv.Atoi(f[1])
		if e != nil {
			continue
		}
		cpu, _ := strconv.ParseFloat(f[2], 64)
		rows = append(rows, processRow{pid: pid, ppid: ppid, cpu: cpu, name: filepath.Base(f[3]), args: strings.Join(f[3:], " ")})
		parents[pid] = ppid
	}
	return rows, parents, scan.Err()
}

func heavyweight(name, args string) bool {
	switch name {
	case "rustc", "xcodebuild", "clang", "clang++", "cc1", "cc1plus", "ld", "ld64", "swift-frontend", "ninja", "make", "cmake":
		return true
	case "cargo":
		return containsWord(args, "build", "test", "check", "clippy", "install")
	case "go":
		return containsWord(args, "build", "test", "install", "generate")
	case "npm", "pnpm", "yarn", "bun", "uv", "pip", "pip3", "brew":
		return containsWord(args, "install", "update", "upgrade", "sync", "build", "test", "ci")
	case "cp", "rsync":
		return strings.Contains(args, "target") || strings.Contains(args, "node_modules") || strings.Contains(args, " -r") || strings.Contains(args, " -R") || strings.Contains(args, " -a") || strings.Contains(args, " -cR")
	case "node", "python", "python3":
		return containsWord(args, "vitest", "jest", "playwright", "pytest", "install", "upgrade", "embedding") || strings.Contains(args, "embedding")
	}
	return strings.HasPrefix(name, "maru_lib-") || strings.HasPrefix(name, "rustdoc")
}

func containsWord(s string, words ...string) bool {
	for _, field := range strings.Fields(s) {
		for _, word := range words {
			if field == word || filepath.Base(field) == word {
				return true
			}
		}
	}
	return false
}

func resolveJobDirectories(ctx context.Context, jobs []HeavyJob) []HeavyJob {
	if runtime.GOOS == "linux" {
		for i := range jobs {
			jobs[i].Directory, _ = os.Readlink(filepath.Join("/proc", strconv.Itoa(jobs[i].PID), "cwd"))
		}
	}
	if runtime.GOOS == "darwin" && len(jobs) > 0 {
		ids := make([]string, 0, len(jobs))
		for _, j := range jobs {
			ids = append(ids, strconv.Itoa(j.PID))
		}
		// One bounded lsof invocation for at most 20 jobs; permission failures
		// leave ownership unknown and therefore block rather than assuming
		// another repo.
		out, err := boundedOutput(ctx, "/usr/sbin/lsof", "-a", "-p", strings.Join(ids, ","), "-d", "cwd", "-Fn")
		cwd := lsofDirectories(out, err)
		for i := range jobs {
			jobs[i].Directory = cwd[jobs[i].PID]
		}
	}
	type identity struct {
		repo    string
		outside bool
	}
	cache := map[string]identity{}
	for i := range jobs {
		dir := jobs[i].Directory
		if dir == "" {
			continue
		}
		id, ok := cache[dir]
		if !ok {
			repo, err := repositoryIdentity(dir)
			id = identity{repo: repo, outside: errors.Is(err, errNotRepository)}
			cache[dir] = id
		}
		jobs[i].Repository = id.repo
		jobs[i].OutsideRepository = id.outside
	}
	return jobs
}

// lsofDirectories trusts only a complete batch: a failed or timed-out run can
// emit valid rows before failing, and every job stays unknown until one
// complete observation.
func lsofDirectories(out string, probeErr error) map[int]string {
	if probeErr != nil {
		return nil
	}
	cwd := map[int]string{}
	pid := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "p") {
			pid, _ = strconv.Atoi(line[1:])
		}
		if strings.HasPrefix(line, "n") && pid > 0 && filepath.IsAbs(line[1:]) {
			cwd[pid] = line[1:]
		}
	}
	return cwd
}

// errNotRepository: the walk reached the filesystem root without a .git. Any
// other resolution failure leaves ownership unknown.
var errNotRepository = errors.New("not inside a Git repository")

// repositoryIdentity resolves dir to its repository's common Git directory
// with bounded filesystem reads, never Git subprocesses, so every worktree of
// one repository has the same identity.
func repositoryIdentity(dir string) (string, error) {
	current, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	current, err = filepath.EvalSymlinks(current)
	if err != nil {
		return "", err
	}
	for depth := 0; depth < 64; depth++ {
		gitPath := filepath.Join(current, ".git")
		info, err := os.Stat(gitPath)
		if err == nil {
			if !info.IsDir() {
				raw, e := readGitPointer(gitPath)
				if e != nil {
					return "", e
				}
				line := strings.TrimSpace(string(raw))
				if !strings.HasPrefix(line, "gitdir: ") {
					return "", fmt.Errorf("invalid Git indirection at %s", gitPath)
				}
				gitPath = strings.TrimSpace(strings.TrimPrefix(line, "gitdir: "))
				if !filepath.IsAbs(gitPath) {
					gitPath = filepath.Join(current, gitPath)
				}
			}
			if raw, e := readGitPointer(filepath.Join(gitPath, "commondir")); e == nil {
				common := strings.TrimSpace(string(raw))
				if !filepath.IsAbs(common) {
					common = filepath.Join(gitPath, common)
				}
				gitPath = common
			} else if !os.IsNotExist(e) {
				return "", e
			}
			return filepath.EvalSymlinks(gitPath)
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("%w: %s", errNotRepository, dir)
		}
		current = parent
	}
	return "", fmt.Errorf("repository ownership unknown for %s: deeper than 64 levels", dir)
}

// readGitPointer reads a Git indirection file: a single short path, never
// bulk data.
func readGitPointer(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 8192 {
		return nil, fmt.Errorf("invalid oversized Git pointer %s", path)
	}
	return os.ReadFile(path)
}
