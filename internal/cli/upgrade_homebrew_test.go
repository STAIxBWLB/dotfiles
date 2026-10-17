package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	osexec "os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/STAIxBWLB/dotfiles/internal/resourceguard"
)

type upgradeReleaseTransport struct{ requests int }

func (tr *upgradeReleaseTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	tr.requests++
	if req.URL.Host == "api.github.com" && strings.HasSuffix(req.URL.Path, "/releases/latest") {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"tag_name":"v99.0.0"}`)), Request: req}, nil
	}
	if req.URL.Host == "github.com" && strings.HasSuffix(req.URL.Path, "/checksums.txt") {
		return &http.Response{StatusCode: http.StatusNotFound, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("not available in fallback fixture")), Request: req}, nil
	}
	return nil, fmt.Errorf("unexpected download attempt: %s", req.URL.Path)
}

func formulaJSON(stable string, pinned bool) string {
	return fmt.Sprintf(`{"formulae":[{"full_name":"staixbwlb/cask/dotfiles","pinned":%v,"versions":{"stable":%q}}]}`, pinned, stable)
}

// fakeUpgrade scripts upgradeRun and upgradeAcquire and records each call as
// "<base name> <args>".
type fakeUpgrade struct {
	calls      []string
	infos      []string // successive brew info outputs; the last one repeats
	version    string   // dot --version output
	acquired   int
	deferErr   error
	launchd    func(args []string) (string, error)
	upgradeErr error  // returned by brew upgrade
	onUpgrade  func() // runs when brew upgrade is called, e.g. to relink
	// canceled lists calls made with a canceled context: brew and the
	// reload must not be killed by a signal (#235 review).
	canceled []string
}

func (f *fakeUpgrade) install(t *testing.T) {
	t.Helper()
	run, acquire, poll := upgradeRun, upgradeAcquire, upgradePoll
	t.Cleanup(func() { upgradeRun, upgradeAcquire, upgradePoll = run, acquire, poll })
	upgradePoll = time.Millisecond
	upgradeAcquire = func(context.Context, resourceguard.Options) (func(), error) {
		if f.deferErr != nil {
			return nil, f.deferErr
		}
		f.acquired++
		return func() {}, nil
	}
	upgradeRun = func(ctx context.Context, _ bool, name string, args ...string) (string, error) {
		call := strings.TrimSpace(filepath.Base(name) + " " + strings.Join(args, " "))
		f.calls = append(f.calls, call)
		if ctx.Err() != nil {
			f.canceled = append(f.canceled, call)
		}
		switch {
		case filepath.Base(name) == "launchctl":
			if f.launchd == nil {
				return "", errors.New("not loaded")
			}
			return f.launchd(args)
		case filepath.Base(name) == "sysctl":
			return "1\n", nil
		case filepath.Base(name) == "uname":
			return "x86_64\n", nil
		case len(args) > 0 && args[0] == "info":
			out := f.infos[0]
			if len(f.infos) > 1 {
				f.infos = f.infos[1:]
			}
			return out, nil
		case len(args) > 0 && args[0] == "--version":
			if !strings.HasSuffix(filepath.ToSlash(name), "/opt/dotfiles/bin/dot") {
				return "dot - graphviz version 12.2.1\n", nil // the version check must read the opt link
			}
			return f.version, nil
		case len(args) > 0 && args[0] == "upgrade":
			if f.onUpgrade != nil {
				f.onUpgrade()
			}
			return "", f.upgradeErr
		}
		return "", nil
	}
}

func (f *fakeUpgrade) brewCalls() []string {
	var out []string
	for _, c := range f.calls {
		if strings.HasPrefix(c, "brew ") || strings.HasPrefix(c, "dot ") {
			out = append(out, c)
		}
	}
	return out
}

// Run the real command from a temporary Cellar binary through a symlink, with
// brew scripted: it upgrades through brew, never downloads or writes the
// Cellar binary, and runs no brew command when already current (#235).
func TestRunUpgradeHomebrew(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("the Homebrew path refuses root")
	}
	if mode := os.Getenv("DOTFILES_TEST_UPGRADE_CHILD"); mode != "" {
		transport := &upgradeReleaseTransport{}
		http.DefaultTransport = transport
		f := &fakeUpgrade{infos: []string{formulaJSON("99.0.0", false)}, version: "dot version 99.0.0 (abc)\n"}
		f.install(t)
		current := "1.0.0"
		if mode == "current" {
			current = "99.0.0"
		}
		cmd := newUpgradeCmd(current)
		cmd.Flags().Bool("dry-run", false, "")
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		if mode == "check" {
			cmd.SetArgs([]string{"--check"})
		}
		if err := cmd.Execute(); err != nil {
			t.Fatalf("%s: %v, output %q", mode, err, out.String())
		}
		var want []string
		if mode == "update" {
			want = []string{"brew info --json=v2 --formula dotfiles", "brew upgrade staixbwlb/cask/dotfiles", "dot --version"}
			if !strings.Contains(out.String(), "Upgraded: 1.0.0 → 99.0.0 (Homebrew)") {
				t.Fatalf("output %q", out.String())
			}
		}
		if got := f.brewCalls(); !slices.Equal(got, want) {
			t.Fatalf("%s: brew calls %q, want %q", mode, got, want)
		}
		wantAcquired := 0
		if mode == "update" {
			wantAcquired = 1
		}
		if f.acquired != wantAcquired {
			t.Fatalf("%s: maintenance slot acquired %d times, want %d", mode, f.acquired, wantAcquired)
		}
		wantRequests := 1
		if mode == "update" && runtime.GOOS == "darwin" || mode == "update" && runtime.GOOS == "linux" {
			wantRequests = 2 // release metadata plus the intentionally missing checksum list
		}
		if transport.requests != wantRequests {
			t.Fatalf("made %d HTTP requests, want %d", transport.requests, wantRequests)
		}
		return
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	binary := filepath.Join(root, "Cellar", "dotfiles", "1.0.0", "bin", "dot")
	if err := os.MkdirAll(filepath.Dir(binary), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, data, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "dot")
	if err := os.Symlink(binary, link); err != nil {
		t.Fatal(err)
	}
	optDir := filepath.Join(root, "opt", "dotfiles")
	if err := os.MkdirAll(filepath.Dir(optDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "Cellar", "dotfiles", "1.0.0"), optDir); err != nil {
		t.Fatal(err)
	}
	before := sha256.Sum256(data)
	for _, mode := range []string{"update", "current", "check"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			child := osexec.CommandContext(ctx, link, "-test.run=^TestRunUpgradeHomebrew$", "-test.count=1")
			child.Env = append(os.Environ(), "DOTFILES_TEST_UPGRADE_CHILD="+mode, "HOME="+t.TempDir())
			if output, err := child.CombinedOutput(); err != nil {
				t.Fatalf("child: %v\n%s", err, output)
			}
			after, err := os.ReadFile(binary)
			if err != nil || sha256.Sum256(after) != before {
				t.Fatalf("binary changed: %v", err)
			}
			entries, err := os.ReadDir(filepath.Dir(binary))
			if err != nil || len(entries) != 1 {
				t.Fatalf("staging files left behind: %v, %v", entries, err)
			}
		})
	}
}

func TestUpgradeHomebrewBranches(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("the Homebrew path refuses root")
	}
	t.Setenv("HOME", t.TempDir()) // no LaunchAgents to reload
	h := homebrewDot{prefix: t.TempDir(), formula: "dotfiles"}
	const done = "dot version 2.0.0 (abc)\n"
	for _, tc := range []struct {
		name     string
		f        fakeUpgrade
		dryRun   bool
		wantErr  string
		want     []string
		wantLine string
	}{
		{
			name: "tap current",
			f:    fakeUpgrade{infos: []string{formulaJSON("2.0.0", false)}, version: done},
			want: []string{"brew info --json=v2 --formula dotfiles", "brew upgrade staixbwlb/cask/dotfiles", "dot --version"},
		},
		{
			name: "tap behind, brew update catches up",
			f:    fakeUpgrade{infos: []string{formulaJSON("1.0.0", false), formulaJSON("2.0.0", false)}, version: done},
			want: []string{"brew info --json=v2 --formula dotfiles", "brew update", "brew info --json=v2 --formula dotfiles", "brew upgrade staixbwlb/cask/dotfiles", "dot --version"},
		},
		{
			name:    "tap still behind after brew update",
			f:       fakeUpgrade{infos: []string{formulaJSON("1.0.0", false)}, version: done},
			wantErr: "not 2.0.0 yet",
			want:    []string{"brew info --json=v2 --formula dotfiles", "brew update", "brew info --json=v2 --formula dotfiles"},
		},
		{
			name:    "pinned",
			f:       fakeUpgrade{infos: []string{formulaJSON("2.0.0", true)}, version: done},
			wantErr: "brew unpin dotfiles",
			want:    []string{"brew info --json=v2 --formula dotfiles"},
		},
		{
			name:    "slot busy",
			f:       fakeUpgrade{infos: []string{formulaJSON("2.0.0", false)}, deferErr: &resourceguard.DeferredError{Reason: "another heavyweight job owns scope maintenance"}},
			wantErr: "maintenance slot",
		},
		{
			name:    "version not reached",
			f:       fakeUpgrade{infos: []string{formulaJSON("2.0.0", false)}, version: "dot version 1.0.0 (abc)\n"},
			wantErr: "not 2.0.0 or newer",
			want:    []string{"brew info --json=v2 --formula dotfiles", "brew upgrade staixbwlb/cask/dotfiles", "dot --version"},
		},
		{
			name:    "brew info lists no formula",
			f:       fakeUpgrade{infos: []string{`{"formulae":[]}`}},
			wantErr: "returned 0 formulae",
			want:    []string{"brew info --json=v2 --formula dotfiles"},
		},
		{
			name:     "a newer release reached the tap",
			f:        fakeUpgrade{infos: []string{formulaJSON("2.1.0", false)}, version: "dot version 2.1.0 (abc)\n"},
			want:     []string{"brew info --json=v2 --formula dotfiles", "brew upgrade staixbwlb/cask/dotfiles", "dot --version"},
			wantLine: "Upgraded: 1.0.0 → 2.1.0 (Homebrew)",
		},
		{
			name:     "dry run",
			f:        fakeUpgrade{infos: []string{formulaJSON("1.0.0", false)}},
			dryRun:   true,
			want:     []string{"brew info --json=v2 --formula dotfiles"},
			wantLine: "[dry-run] would run: brew update\n[dry-run] would run: brew upgrade staixbwlb/cask/dotfiles\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := tc.f
			f.install(t)
			var out bytes.Buffer
			err := upgradeHomebrew(context.Background(), &Printer{Out: &out, Err: &out}, h, "1.0.0", "2.0.0", tc.dryRun)
			if tc.wantErr == "" && err != nil || tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("err = %v, want %q", err, tc.wantErr)
			}
			if got := f.brewCalls(); !slices.Equal(got, tc.want) {
				t.Fatalf("calls %q, want %q", got, tc.want)
			}
			if tc.dryRun && f.acquired != 0 {
				t.Fatal("dry run took the maintenance slot")
			}
			if !strings.Contains(out.String(), tc.wantLine) {
				t.Fatalf("output %q, want %q", out.String(), tc.wantLine)
			}
		})
	}
}

// A signal cancels dot's context, but brew must finish instead of being
// SIGKILLed mid-link; it gets the terminal's SIGINT itself (#235 review).
func TestUpgradeHomebrewSignalDoesNotKillBrew(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("the Homebrew path refuses root")
	}
	t.Setenv("HOME", t.TempDir())
	f := &fakeUpgrade{infos: []string{formulaJSON("1.0.0", false), formulaJSON("2.0.0", false)}, version: "dot version 2.0.0 (abc)\n"}
	f.install(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out bytes.Buffer
	if err := upgradeHomebrew(ctx, &Printer{Out: &out, Err: &out}, homebrewDot{prefix: t.TempDir(), formula: "dotfiles"}, "1.0.0", "2.0.0", false); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.canceled {
		if !strings.HasPrefix(c, "brew info") {
			t.Errorf("%q ran with a canceled context", c)
		}
	}
}

// recordedSyncPrint is `launchctl print gui/501/com.dotfiles.sync` from a work
// Mac (2026-10-02, dot 2.70.31), home path shortened; <PROGRAM> marks the
// top-level program line the reload matches.
const recordedSyncPrint = `gui/501/com.dotfiles.sync = {
	active count = 0
	path = /Users/u/Library/LaunchAgents/com.dotfiles.sync.plist
	type = LaunchAgent
	state = not running

	program = <PROGRAM>
	arguments = {
		<PROGRAM>
		sync
		push
		--mode=clean
	}

	stdout path = /Users/u/workspace/work/.dotfiles/sync/log/sync.log
	stderr path = /Users/u/workspace/work/.dotfiles/sync/log/sync.log
	inherited environment = {
		SSH_AUTH_SOCK => /var/run/com.apple.launchd.xFraF380bi/Listeners
	}

	default environment = {
		PATH => /usr/bin:/bin:/usr/sbin:/sbin
	}

	environment = {
		OSLogRateLimit => 64
		PATH => /opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin
		XPC_SERVICE_NAME => com.dotfiles.sync
	}

	domain = gui/501 [100021]
	asid = 100021
	minimum runtime = 10
	exit timeout = 5
	runs = 86
	last exit code = 0

	spawn type = daemon (3)
	run interval = 300 seconds
	job state = exited
	sanitizer flags = 0x0

	properties = runatload | inferred program
}
`

// After the upgrade, a loaded job running the Cellar binary is booted out and
// bootstrapped; an unloaded job (a paused sync) and a job running another
// binary are left alone (#235).
func TestReloadDotLaunchAgents(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("launchd only")
	}
	if os.Geteuid() == 0 {
		t.Skip("the Homebrew path refuses root")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := homebrewDot{prefix: filepath.Join(root, "brew"), formula: "dotfiles"}
	kegDot := filepath.Join(h.prefix, "Cellar", "dotfiles", "2.0.0", "bin", "dot")
	newKeg := filepath.Join(h.prefix, "Cellar", "dotfiles", "2.1.0", "bin", "dot")
	linked := filepath.Join(h.prefix, "bin", "dot")
	opt := filepath.Join(h.prefix, "opt", "dotfiles")
	other := filepath.Join(root, "other", "dot")
	// link points bin/dot at the keg's binary and opt/dotfiles at the keg, as
	// brew does; binOwner, when set, owns bin/dot instead (graphviz).
	link := func(keg, binOwner string) {
		for _, l := range []struct{ path, target string }{{linked, keg}, {opt, filepath.Dir(filepath.Dir(keg))}} {
			if l.path == linked && binOwner != "" {
				l.target = binOwner
			}
			_ = os.Remove(l.path)
			if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(l.target, l.path); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, f := range []string{kegDot, newKeg, other} {
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, nil, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	home := filepath.Join(root, "home")
	agents := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(agents, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, label := range []string{"com.dotfiles.sync", "com.dotfiles.peer", "com.dotfiles.other", "com.dotfiles.broken"} {
		if err := os.WriteFile(filepath.Join(agents, label+".plist"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	domain := guiTarget()
	reloaded := []string{"com.dotfiles.broken", "com.dotfiles.sync"}

	for _, tc := range []struct {
		name          string
		run           func(ctx context.Context, p *Printer) error
		cancel        bool
		relink        bool // brew upgrade links a new keg
		binTaken      bool // another formula owns bin/dot; jobs run opt/dotfiles/bin/dot
		upgradeFail   bool
		bootstrapFail bool
		stuck         bool // the job never leaves the domain after bootout
		wantErr       string
		wantLine      string
		attempts      int // bootstraps per reloaded job; 0 means none, nor any bootout
	}{
		{name: "reloads", attempts: 1, wantLine: "Reloaded com.dotfiles.sync"},
		{name: "a signal does not cut the reload short", cancel: true, attempts: 1, wantLine: "Reloaded com.dotfiles.sync"},
		{name: "bootstrap fails", bootstrapFail: true, attempts: 3, wantErr: "could not reload com.dotfiles.broken, com.dotfiles.sync"},
		{name: "still loaded after bootout", stuck: true, wantErr: "could not reload com.dotfiles.broken, com.dotfiles.sync", wantLine: "still loaded after bootout"},
		{
			name:     "dry run",
			run:      func(ctx context.Context, p *Printer) error { return reloadDotLaunchAgents(ctx, p, h, true) },
			wantLine: "[dry-run] would reload com.dotfiles.sync",
		},
		{
			// The reload also follows a failed post-upgrade check: brew may
			// already have replaced the binary.
			name: "after a failed version check",
			run: func(ctx context.Context, p *Printer) error {
				return upgradeHomebrew(ctx, p, h, "1.0.0", "2.0.0", false)
			},
			relink:   true,
			attempts: 1,
			wantErr:  "not 2.0.0 or newer",
			wantLine: "Reloaded com.dotfiles.sync",
		},
		{
			name: "after a failed brew upgrade",
			run: func(ctx context.Context, p *Printer) error {
				return upgradeHomebrew(ctx, p, h, "1.0.0", "2.0.0", false)
			},
			relink:      true,
			upgradeFail: true,
			attempts:    1,
			wantErr:     "brew upgrade staixbwlb/cask/dotfiles: exit status 1",
			wantLine:    "Reloaded com.dotfiles.sync",
		},
		{
			// bin/dot belongs to graphviz, so only the opt link moves.
			name: "when another formula owns bin/dot",
			run: func(ctx context.Context, p *Printer) error {
				return upgradeHomebrew(ctx, p, h, "1.0.0", "2.0.0", false)
			},
			relink:   true,
			binTaken: true,
			attempts: 1,
			wantErr:  "not 2.0.0 or newer",
			wantLine: "Reloaded com.dotfiles.sync",
		},
		{
			// A brew upgrade that failed without relinking changed nothing,
			// so the healthy jobs keep running.
			name: "after a failed brew upgrade that kept the link",
			run: func(ctx context.Context, p *Printer) error {
				return upgradeHomebrew(ctx, p, h, "1.0.0", "2.0.0", false)
			},
			upgradeFail: true,
			wantErr:     "brew upgrade staixbwlb/cask/dotfiles: exit status 1",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			binOwner, syncProgram := "", linked
			if tc.binTaken {
				binOwner, syncProgram = other, filepath.Join(opt, "bin", "dot")
			}
			link(kegDot, binOwner)
			loaded := map[string]string{
				"com.dotfiles.sync":   syncProgram,
				"com.dotfiles.broken": kegDot,
				"com.dotfiles.other":  other,
				// com.dotfiles.peer is not loaded, like a paused job
			}
			f := &fakeUpgrade{infos: []string{formulaJSON("2.0.0", false)}, version: "dot version 1.0.0 (abc)\n", launchd: func(args []string) (string, error) {
				label := strings.TrimPrefix(args[len(args)-1], domain+"/")
				switch args[0] {
				case "print":
					if program, ok := loaded[label]; ok {
						return strings.ReplaceAll(recordedSyncPrint, "<PROGRAM>", program), nil
					}
					return "", errors.New("Could not find service")
				case "bootout":
					if !tc.stuck {
						delete(loaded, label)
					}
					return "", nil
				case "bootstrap":
					if tc.bootstrapFail {
						return "", errors.New("Bootstrap failed: 5: Input/output error")
					}
					return "", nil
				}
				return "", fmt.Errorf("unexpected launchctl %v", args)
			}}
			if tc.upgradeFail {
				f.upgradeErr = errors.New("exit status 1")
			}
			if tc.relink {
				f.onUpgrade = func() { link(newKeg, binOwner) }
			}
			f.install(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancel {
				cancel()
			}
			var out bytes.Buffer
			p := &Printer{Out: &out, Err: &out}
			run := tc.run
			if run == nil {
				run = func(ctx context.Context, p *Printer) error { return reloadDotLaunchAgents(ctx, p, h, false) }
			}
			err := run(ctx, p)
			if tc.wantErr == "" && err != nil || tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("err = %v, want %q\n%s", err, tc.wantErr, out.String())
			}
			var mutations []string
			for _, c := range f.calls {
				if strings.HasPrefix(c, "launchctl bootout") || strings.HasPrefix(c, "launchctl bootstrap") {
					mutations = append(mutations, c)
				}
			}
			var want []string
			for _, label := range reloaded {
				if tc.attempts == 0 && !tc.stuck {
					break
				}
				want = append(want, "launchctl bootout "+domain+"/"+label)
				for range tc.attempts {
					want = append(want, "launchctl bootstrap "+domain+" "+filepath.Join(agents, label+".plist"))
				}
			}
			if !slices.Equal(mutations, want) {
				t.Fatalf("launchctl mutations %q, want %q", mutations, want)
			}
			if !strings.Contains(out.String(), tc.wantLine) {
				t.Fatalf("output %q, want %q", out.String(), tc.wantLine)
			}
			if len(f.canceled) > 0 {
				t.Fatalf("ran with a canceled context: %q", f.canceled)
			}
		})
	}
}

func TestLaunchdProgram(t *testing.T) {
	if got := launchdProgram(strings.ReplaceAll(recordedSyncPrint, "<PROGRAM>", "/opt/homebrew/bin/dot")); got != "/opt/homebrew/bin/dot" {
		t.Fatalf("launchdProgram = %q", got)
	}
	if got := launchdProgram("\t\tprogram = /nested\n"); got != "" {
		t.Fatalf("nested program matched: %q", got)
	}
}
