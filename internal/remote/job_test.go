package remote

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/admission"
	"github.com/STAIxBWLB/dotfiles/internal/config"
	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

func jobState(job config.UserRemoteJob) config.UserRemoteState {
	host := validHost()
	host.Jobs = map[string]config.UserRemoteJob{"media": job}
	return stateWith(host)
}

func shellRun(script string) (string, error) {
	out, err := osexec.Command("bash", "-c", script).CombinedOutput()
	return string(out), err
}

func refJob() config.UserRemoteJob {
	return config.UserRemoteJob{Image: config.UserRemoteImage{Ref: "ghcr.io/org/media:1"}}
}

func TestJobProfileDefaultsAndTemplating(t *testing.T) {
	job := refJob()
	job.CPUs = 6
	job.Env = map[string]string{"WORKER_THREADS": "{half_cpus}", "ALL": "{cpus}-x"}
	job.Local = config.UserRemoteJobLocal{Path: []string{"~/tools/bin"}, Env: map[string]string{"QA_OFFLINE": "1"}}
	cfg, err := Load(jobState(job), "/home/u")
	if err != nil {
		t.Fatal(err)
	}
	p, err := cfg.Hosts["buildbox"].Profile("")
	if err != nil {
		t.Fatal(err)
	}
	if p.Root != "dot-remote-jobs/media" || p.MaxJobs != 1 || p.Fallback != FallbackLocal || p.Memory != DefaultMemory {
		t.Errorf("defaults = %+v", p)
	}
	if p.LocalRoot != "/home/u/.local/state/dot/remote-jobs/media" || p.LocalPath[0] != "/home/u/tools/bin" {
		t.Errorf("local = %q %v", p.LocalRoot, p.LocalPath)
	}
	env := map[string]string{}
	for _, pair := range p.JobEnv(p.CPUs) {
		env[pair.Key] = pair.Value
	}
	if env["WORKER_THREADS"] != "3" || env["ALL"] != "6-x" {
		t.Errorf("env = %v", env)
	}
	if got := p.JobEnv(1); got[1].Value != "1" {
		t.Errorf("half of 1 CPU = %v, want at least 1", got)
	}
}

func TestJobProfileValidationNamesTheKey(t *testing.T) {
	neg := -1
	zero := 0
	ratio := 0.0
	cases := map[string]func(*config.UserRemoteJob){
		"jobs.media.image":          func(j *config.UserRemoteJob) { j.Image = config.UserRemoteImage{} },
		"jobs.media.max_jobs":       func(j *config.UserRemoteJob) { j.MaxJobs = &zero },
		"jobs.media.max_load_ratio": func(j *config.UserRemoteJob) { j.MaxLoadRatio = &ratio },
		"jobs.media.min_free_gb":    func(j *config.UserRemoteJob) { j.MinFreeGB = &neg },
		"jobs.media.root":           func(j *config.UserRemoteJob) { j.Root = "a/../b" },
		"jobs.media.fallback":       func(j *config.UserRemoteJob) { j.Fallback = "maybe" },
		"jobs.media.env.X":          func(j *config.UserRemoteJob) { j.Env = map[string]string{"X": "{gpus}"} },
		"jobs.media.memory":         func(j *config.UserRemoteJob) { j.Memory = "lots" },
		"jobs.media.local.root":     func(j *config.UserRemoteJob) { j.Local.Root = "relative" },
	}
	for want, mutate := range cases {
		job := refJob()
		mutate(&job)
		if _, err := Load(jobState(job), "/home/u"); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v", want, err)
		}
	}
	job := refJob()
	job.Root = "/data/me/jobs"
	if _, err := Load(jobState(job), "/home/u"); err != nil {
		t.Errorf("an absolute root was refused: %v", err)
	}
}

func TestJobNameAndInputs(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if got, _ := JobName("", now); got != "260102-030405" {
		t.Errorf("default name = %q", got)
	}
	for _, bad := range []string{"Upper", "-x", "a_b", strings.Repeat("a", 65)} {
		if _, err := JobName(bad, now); err == nil {
			t.Errorf("name %q accepted", bad)
		}
	}
	dir := t.TempDir()
	a := filepath.Join(dir, "a", "clip.wav")
	b := filepath.Join(dir, "b", "clip.wav")
	for _, p := range []string{a, b} {
		write(t, p, "x")
	}
	if _, err := CheckInputs([]string{a, b}); err == nil || !strings.Contains(err.Error(), "share the name clip.wav") {
		t.Errorf("duplicate names: %v", err)
	}
	if _, err := CheckInputs([]string{a, filepath.Join(dir, "nope")}); err == nil || !strings.Contains(err.Error(), "missing input") {
		t.Errorf("missing input: %v", err)
	}
	if got, err := CheckInputs([]string{a}); err != nil || got[0] != a {
		t.Errorf("CheckInputs = %v, %v", got, err)
	}
}

func TestJobContainerArgvHasNoCachesAndMountsTheJob(t *testing.T) {
	h := testHost()
	h.Caches = []Cache{{Name: "cargo", Target: "/c"}}
	p := JobProfile{Name: "media", Root: "/data/jobs", Ref: "img:1", CPUs: 8, Memory: "16g", Env: []EnvPair{{Key: "T", Value: "{half_cpus}"}}}
	cmd := JobRunCommand(h, p, "j1", []string{"ffmpeg -i in/a out/b"})
	for _, want := range []string{"run --rm -v '/data/jobs/j1':/job", "--name me-rdev-media-j1", "--cpus 8", "--memory 16g", "-w /job", "-e T=4", "img:1", "'ffmpeg -i in/a out/b'"} {
		if !strings.Contains(cmd, want) {
			t.Errorf("job command lacks %q:\n%s", want, cmd)
		}
	}
	if strings.Contains(cmd, "-c") && strings.Contains(cmd, ":/c") {
		t.Errorf("job mounts a cache: %s", cmd)
	}
	rel := JobRunCommand(h, JobProfile{Name: "media", Root: "jobs", Memory: "1g"}, "j1", []string{"true"})
	if !strings.Contains(rel, `-v "$HOME"/'jobs/j1':/job`) {
		t.Errorf("relative root: %s", rel)
	}
	if built := (JobProfile{Name: "media", Dockerfile: "/x"}).ImageHost(h).Image(); built != "me-rdev-media:latest" {
		t.Errorf("built profile image = %q", built)
	}
}

func TestParseJobCheck(t *testing.T) {
	if ok, why := ParseJobCheck("ok\n"); !ok || why != "ok" {
		t.Errorf("ok = %v %q", ok, why)
	}
	if ok, why := ParseJobCheck("unavailable\tload1 9 is at or over 0.7 x 4 CPUs\n"); ok || !strings.Contains(why, "load1 9") {
		t.Errorf("load = %v %q", ok, why)
	}
	if ok, _ := ParseJobCheck(""); ok {
		t.Error("no answer read as available")
	}
	if ok, _ := ParseJobCheck("garbage"); ok {
		t.Error("garbage read as available")
	}
}

// The check script runs on a real shell with the runtime stubbed, so its
// branches are the ones the host takes.
func TestJobCheckScriptOnAShell(t *testing.T) {
	h := testHost()
	p := JobProfile{Name: "media", Root: t.TempDir(), Ref: "img:1", MinFreeGB: 0}
	run := func(stub string, p JobProfile) string {
		out, err := shellRun("docker(){ " + stub + " }; " + JobCheckScript(h, p))
		if err != nil {
			t.Fatalf("%v: %s", err, out)
		}
		return out
	}
	if ok, why := ParseJobCheck(run("echo dev;", p)); !ok {
		t.Errorf("healthy host unavailable: %s", why)
	}
	if ok, why := ParseJobCheck(run("return 1;", p)); ok || !strings.Contains(why, "missing") {
		t.Errorf("missing image: %v %q", ok, why)
	}
	if ok, why := ParseJobCheck(run("echo root;", p)); ok || !strings.Contains(why, "root") {
		t.Errorf("root image: %v %q", ok, why)
	}
	p.MinFreeGB = 1 << 30
	if ok, why := ParseJobCheck(run("echo dev;", p)); ok || !strings.Contains(why, "GB free") {
		t.Errorf("disk: %v %q", ok, why)
	}
}

// jobFixture fakes ssh, rsync and the runtime for one job.
type jobFixture struct {
	t        *testing.T
	root     string
	store    *admission.Store
	cfg      Config
	check    string
	checkErr error
	exit     int
	sig      syscall.Signal
	pullErr  error
	local    int
	exists   bool
	outLink  bool
	calls    []string
	localArg []string
	localDir string
	out, err strings.Builder
}

func newJobFixture(t *testing.T) *jobFixture {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := testHost()
	h.Jobs = map[string]JobProfile{"media": {Name: "media", Root: "jobs", Ref: "img:1", Memory: "1g", MaxJobs: 1, Fallback: FallbackLocal, LocalRoot: filepath.Join(root, "local")}}
	return &jobFixture{
		t: t, root: root, check: "ok\n",
		store: admission.NewStore(filepath.Join(root, "admission"), exec.NewProbeRunner()),
		cfg:   Config{Hosts: map[string]Host{h.Name: h}, Default: h.Name},
	}
}

func (f *jobFixture) deps() Deps {
	return Deps{
		Store: f.store, Runner: exec.NewProbeRunner(), LocalRsync: "rsync", Out: &f.out, Err: &f.err,
		DotBinary: "dot",
		Exec: func(_ context.Context, argv []string, _ io.Reader, stdout, _ io.Writer) (int, syscall.Signal, error) {
			last := argv[len(argv)-1]
			f.calls = append(f.calls, strings.Join(argv, " "))
			switch {
			case strings.Contains(last, "printf 'ok\\n'"):
				if f.checkErr != nil {
					return 255, 0, f.checkErr
				}
				fmt.Fprint(stdout, f.check)
			case strings.Contains(last, "then echo dir; fi"):
				if !f.outLink {
					fmt.Fprintln(stdout, "dir")
				}
			case strings.Contains(last, "echo exists"):
				if f.exists {
					fmt.Fprintln(stdout, "exists")
				}
			case argv[0] == "rsync" && strings.HasSuffix(last, "/") && strings.Contains(argv[len(argv)-2], "/out/"):
				if f.pullErr != nil {
					return 23, 0, f.pullErr
				}
			case strings.Contains(last, " run --rm "):
				return f.exit, f.sig, nil
			}
			return 0, 0, nil
		},
		ExecLocal: func(_ context.Context, argv []string, dir string, _ []string, _ io.Reader, _, _ io.Writer) (int, syscall.Signal, error) {
			f.localArg, f.localDir = argv, dir
			write(f.t, filepath.Join(dir, "out", "result.txt"), "local")
			return f.local, 0, nil
		},
	}
}

func (f *jobFixture) run(opts JobOptions) error {
	if opts.OutDir == "" {
		opts.OutDir = filepath.Join(f.root, "result")
	}
	if opts.Name == "" {
		opts.Name = "j1"
	}
	if len(opts.Command) == 0 {
		opts.Command = []string{"cp in/a.txt out/"}
	}
	return f.deps().RunJob(context.Background(), f.cfg, opts)
}

func (f *jobFixture) called(sub string) bool {
	for _, c := range f.calls {
		if strings.Contains(c, sub) {
			return true
		}
	}
	return false
}

func TestRemoteJobDeletesItsDirectoryOnlyOnSuccess(t *testing.T) {
	f := newJobFixture(t)
	in := filepath.Join(f.root, "a.txt")
	write(t, in, "a")
	if err := f.run(JobOptions{Inputs: []string{in}}); err != nil {
		t.Fatalf("RunJob = %v\n%s", err, f.err.String())
	}
	if !f.called(in+" buildbox:jobs/j1/in/") || !f.called("buildbox:jobs/j1/out/ "+filepath.Join(f.root, "result")+"/") {
		t.Errorf("transfers: %v", f.calls)
	}
	if !f.called(`rm -rf "$HOME"/'jobs/j1'`) {
		t.Errorf("a successful job was not deleted: %v", f.calls)
	}

	f = newJobFixture(t)
	f.exit = 3
	err := f.run(JobOptions{})
	var code *ExitCode
	if !errors.As(err, &code) || code.Code != 3 || f.called("rm -rf") || !strings.Contains(f.err.String(), "job kept at buildbox:~/jobs/j1") {
		t.Errorf("failed job: %v, rm=%v, %s", err, f.called("rm -rf"), f.err.String())
	}

	f = newJobFixture(t)
	f.pullErr = errors.New("rsync exited 23")
	if err := f.run(JobOptions{}); err == nil || f.called("rm -rf") {
		t.Errorf("failed collection: %v, rm=%v", err, f.called("rm -rf"))
	}
}

func TestRemoteJobInterruptRemovesTheContainer(t *testing.T) {
	f := newJobFixture(t)
	f.exit, f.sig = 130, syscall.SIGINT
	err := f.run(JobOptions{})
	var code *ExitCode
	if !errors.As(err, &code) || code.Code != 130 || !f.called("docker rm -f 'me-rdev-media-j1'") {
		t.Errorf("interrupt: %v, %v", err, f.calls)
	}
}

func TestJobFallsBackLocallyOrExits69(t *testing.T) {
	f := newJobFixture(t)
	f.check = "unavailable\tload too high\n"
	in := filepath.Join(f.root, "a.txt")
	write(t, in, "a")
	if err := f.run(JobOptions{Inputs: []string{in}}); err != nil {
		t.Fatalf("fallback = %v", err)
	}
	if f.localArg[0] != "dot" || f.localArg[1] != "admit" || !strings.Contains(f.err.String(), "running it locally") {
		t.Errorf("local argv %v, stderr %s", f.localArg, f.err.String())
	}
	if got, _ := os.ReadFile(filepath.Join(f.root, "result", "result.txt")); string(got) != "local" {
		t.Errorf("out/ was not copied: %q", got)
	}
	if _, err := os.Stat(f.localDir); !os.IsNotExist(err) {
		t.Errorf("a successful local job kept %s", f.localDir)
	}

	f = newJobFixture(t)
	f.check = "unavailable\tload too high\n"
	f.local = 2
	if err := f.run(JobOptions{}); err == nil {
		t.Error("a failed local job succeeded")
	}
	if _, err := os.Stat(f.localDir); err != nil {
		t.Errorf("a failed local job was not kept: %v", err)
	}

	f = newJobFixture(t)
	f.checkErr = errors.New("ssh exited 255")
	err := f.run(JobOptions{RemoteOnly: true})
	var code *Unavailable
	if !errors.As(err, &code) || code.ExitCode() != ExitUnavailable || f.localArg != nil {
		t.Errorf("--remote-only = %v, local %v", err, f.localArg)
	}
}

func TestJobLocalOnlySkipsTheHost(t *testing.T) {
	f := newJobFixture(t)
	if err := f.run(JobOptions{LocalOnly: true}); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 0 || f.localArg == nil {
		t.Errorf("--local-only reached the host: %v", f.calls)
	}
}

func TestSecondJobDefersWith75(t *testing.T) {
	f := newJobFixture(t)
	h := f.cfg.Hosts["buildbox"]
	held, err := f.deps().acquireJob(context.Background(), h, h.Jobs["media"], 0)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	err = f.run(JobOptions{})
	var deferred *Deferred
	if !errors.As(err, &deferred) || deferred.Outcome.Scope != JobScope(h, "media") || len(f.calls) != 0 {
		t.Errorf("second job = %v, calls %v", err, f.calls)
	}
}

// Collecting into DIR never follows what DIR already holds: a symlinked file
// or directory there is replaced, as rsync replaces it, and nothing outside
// DIR is written.
func TestCopyTreeDoesNotFollowSymlinksInTheDestination(t *testing.T) {
	root := t.TempDir()
	src, dst, outside := filepath.Join(root, "src"), filepath.Join(root, "dst"), filepath.Join(root, "outside")
	write(t, filepath.Join(src, "result.txt"), "new")
	write(t, filepath.Join(src, "sub", "f.txt"), "new")
	write(t, filepath.Join(outside, "victim.txt"), "keep")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "victim.txt"), filepath.Join(dst, "result.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dst, "sub")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dst, "other.txt"), "left alone")
	if err := copyTree(src, dst, ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(outside, "victim.txt")); string(got) != "keep" {
		t.Errorf("a file outside DIR was overwritten: %q", got)
	}
	if _, err := os.Stat(filepath.Join(outside, "f.txt")); err == nil {
		t.Error("a file landed outside DIR through a symlinked directory")
	}
	for rel, want := range map[string]string{"result.txt": "new", "sub/f.txt": "new", "other.txt": "left alone"} {
		info, err := os.Lstat(filepath.Join(dst, rel))
		got, _ := os.ReadFile(filepath.Join(dst, rel))
		if err != nil || info.Mode()&os.ModeSymlink != 0 || string(got) != want {
			t.Errorf("%s = %q (%v)", rel, got, err)
		}
	}
}

func TestLocalJobRefusesAnOutputInsideItsDirectory(t *testing.T) {
	f := newJobFixture(t)
	work := filepath.Join(f.root, "local", "j1")
	for _, out := range []string{work, filepath.Join(work, "out"), filepath.Join(work, "x", "y")} {
		err := f.run(JobOptions{LocalOnly: true, OutDir: out})
		if err == nil || !strings.Contains(err.Error(), "inside the local job directory") || f.localArg != nil {
			t.Errorf("-o %s = %v", out, err)
		}
	}
}

func TestAnExistingJobNameIsRefused(t *testing.T) {
	f := newJobFixture(t)
	f.exists = true
	err := f.run(JobOptions{})
	if err == nil || !strings.Contains(err.Error(), "job j1 already exists at buildbox:~/jobs/j1") || f.called(" run --rm ") || f.called("rm -rf") {
		t.Errorf("remote reuse = %v, calls %v", err, f.calls)
	}
	f = newJobFixture(t)
	if err := os.MkdirAll(filepath.Join(f.root, "local", "j1", "out"), 0o755); err != nil {
		t.Fatal(err)
	}
	err = f.run(JobOptions{LocalOnly: true})
	if err == nil || !strings.Contains(err.Error(), "already exists") || f.localArg != nil {
		t.Errorf("local reuse = %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.root, "local", "j1", "out")); err != nil {
		t.Errorf("the existing job directory was touched: %v", err)
	}
}

func TestJobCreateScriptIsExclusive(t *testing.T) {
	root := t.TempDir()
	p := JobProfile{Root: filepath.Join(root, "jobs")}
	for i, want := range []string{"", "exists"} {
		out, err := shellRun(jobCreateScript(p, "j"))
		if err != nil || strings.TrimSpace(out) != want {
			t.Errorf("run %d: %q %v", i, out, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "jobs", "j", "in")); err != nil {
		t.Error(err)
	}
}

// A symlinked input travels as its contents under the link's own name.
func TestSymlinkedInputIsSentAsContents(t *testing.T) {
	f := newJobFixture(t)
	real := filepath.Join(f.root, "data", "real.wav")
	write(t, real, "x")
	link := filepath.Join(f.root, "clip.wav")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err := f.run(JobOptions{Inputs: []string{link}}); err != nil {
		t.Fatal(err)
	}
	if !f.called("rsync -a -L -s -e ") || !f.called(" "+link+" buildbox:jobs/j1/in/") {
		t.Errorf("staging: %v", f.calls)
	}
}

// A directory already in DIR where the job wrote a file is never removed: the
// collection stops, names it, and the job stays.
func TestCopyTreeKeepsADirectoryWhereTheJobWroteAFile(t *testing.T) {
	root := t.TempDir()
	src, dst := filepath.Join(root, "src"), filepath.Join(root, "dst")
	write(t, filepath.Join(src, "report"), "job")
	write(t, filepath.Join(dst, "report", "notes.txt"), "mine")
	err := copyTree(src, dst, "")
	if err == nil || !strings.Contains(err.Error(), "is a directory, the job wrote a file there") {
		t.Fatalf("copyTree = %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(dst, "report", "notes.txt")); string(got) != "mine" {
		t.Errorf("a user file was deleted: %q", got)
	}

	f := newJobFixture(t)
	write(t, filepath.Join(f.root, "result", "result.txt", "notes.txt"), "mine")
	if err := f.run(JobOptions{LocalOnly: true}); err == nil {
		t.Error("the collection conflict was not reported")
	}
	if _, err := os.Stat(f.localDir); err != nil {
		t.Errorf("the job was not kept: %v", err)
	}
}

// A job that replaced out/ with a symlink collects nothing.
func TestRemoteJobRefusesASymlinkedOut(t *testing.T) {
	f := newJobFixture(t)
	f.outLink = true
	err := f.run(JobOptions{})
	if err == nil || !strings.Contains(err.Error(), "is not a directory (a symlink?)") || f.called("/out/ ") || f.called("rm -rf") {
		t.Errorf("symlinked out: %v, %v", err, f.calls)
	}
	if script := outIsDirScript(JobProfile{Root: "jobs"}, "j"); !strings.Contains(script, `[ ! -L "$HOME"/'jobs/j'/out ]`) {
		t.Errorf("probe = %s", script)
	}
	dir := t.TempDir()
	p := JobProfile{Root: dir}
	if err := os.MkdirAll(filepath.Join(dir, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "j"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "real"), filepath.Join(dir, "j", "out")); err != nil {
		t.Fatal(err)
	}
	if out, _ := shellRun(outIsDirScript(p, "j")); strings.TrimSpace(out) == "dir" {
		t.Error("a symlinked out/ passed the probe")
	}
}

// A failed command's own status wins over a collection error.
func TestLocalJobKeepsTheCommandStatus(t *testing.T) {
	f := newJobFixture(t)
	f.local = 7
	write(t, filepath.Join(f.root, "result", "result.txt", "x"), "mine")
	err := f.run(JobOptions{LocalOnly: true})
	var code *ExitCode
	if !errors.As(err, &code) || code.Code != 7 || !strings.Contains(f.err.String(), "copying out/") {
		t.Errorf("err = %v, stderr %s", err, f.err.String())
	}
}

// runLocal runs a --local-only job whose command is fn, run in the job dir.
func (f *jobFixture) runLocal(t *testing.T, outDir string, fn func(work string)) error {
	t.Helper()
	d := f.deps()
	d.ExecLocal = func(_ context.Context, _ []string, dir string, _ []string, _ io.Reader, _, _ io.Writer) (int, syscall.Signal, error) {
		f.localDir = dir
		fn(dir)
		return 0, 0, nil
	}
	return d.RunJob(context.Background(), f.cfg, JobOptions{LocalOnly: true, Name: "j1", OutDir: outDir, Command: []string{"x"}})
}

// A job that leaves out as a symlink collects nothing and is kept, so the
// directory it points into is not deleted with the job.
func TestLocalJobRefusesASymlinkedOut(t *testing.T) {
	f := newJobFixture(t)
	err := f.runLocal(t, filepath.Join(f.root, "result"), func(work string) {
		write(t, filepath.Join(work, "generated", "r.txt"), "made")
		_ = os.RemoveAll(filepath.Join(work, "out"))
		_ = os.Symlink(filepath.Join(work, "generated"), filepath.Join(work, "out"))
	})
	if err == nil || !strings.Contains(err.Error(), "is not a directory (a symlink?)") {
		t.Fatalf("err = %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(f.localDir, "generated", "r.txt")); string(got) != "made" {
		t.Error("the job and what it made were deleted")
	}
}

// An unreadable output never costs the old file in DIR.
func TestCopyTreeKeepsTheOldFileWhenTheNewOneCannotBeRead(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a 000 file")
	}
	root := t.TempDir()
	src, dst := filepath.Join(root, "src"), filepath.Join(root, "dst")
	write(t, filepath.Join(src, "r.txt"), "new")
	if err := os.Chmod(filepath.Join(src, "r.txt"), 0); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dst, "r.txt"), "old")
	if err := copyTree(src, dst, ""); err == nil {
		t.Error("an unreadable output was collected")
	}
	if got, _ := os.ReadFile(filepath.Join(dst, "r.txt")); string(got) != "old" {
		t.Errorf("the old file was lost: %q", got)
	}
	entries, _ := os.ReadDir(dst)
	if len(entries) != 1 {
		t.Errorf("a temporary file was left: %v", entries)
	}
}

// -o at the local job root (an ancestor of the job dir) still works, but a
// job that writes out/<its own name>/ would merge into itself: refused, kept.
func TestCopyTreeRefusesTargetsInsideTheJobDirectory(t *testing.T) {
	f := newJobFixture(t)
	localRoot := filepath.Join(f.root, "local")
	err := f.runLocal(t, localRoot, func(work string) {
		write(t, filepath.Join(work, "out", "j1", "r.txt"), "made")
	})
	if err == nil || !strings.Contains(err.Error(), "resolves into the job directory") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.localDir, "out", "j1", "r.txt")); err != nil {
		t.Errorf("the job was not kept: %v", err)
	}

	f = newJobFixture(t)
	if err := f.runLocal(t, filepath.Join(f.root, "local"), func(work string) {
		write(t, filepath.Join(work, "out", "other.txt"), "ok")
	}); err != nil {
		t.Fatalf("-o at the local root: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(f.root, "local", "other.txt")); string(got) != "ok" {
		t.Errorf("collected = %q", got)
	}
}
