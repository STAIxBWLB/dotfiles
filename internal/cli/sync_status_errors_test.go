package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestStatusCommandsReportRefusedConflictDirectory(t *testing.T) {
	_, root := goldenSyncFixture(t)
	link := filepath.Join(root, ".sync-conflicts")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"sync", "peer"} {
		t.Run(command, func(t *testing.T) {
			out, stderr, err := runDotForTest(command, "status", "--json")
			if err != nil {
				t.Fatalf("status: %v, %s", err, stderr)
			}
			var document map[string]json.RawMessage
			if err := json.Unmarshal([]byte(out), &document); err != nil {
				t.Fatal(err)
			}
			if command == "peer" {
				var profile map[string]json.RawMessage
				if err := json.Unmarshal(document["profile"], &profile); err != nil {
					t.Fatal(err)
				}
				document = profile
			}
			var message string
			if err := json.Unmarshal(document["conflictsError"], &message); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(message, ".sync-conflicts") || !strings.Contains(message, "not a plain directory") {
				t.Fatalf("conflictsError = %q", message)
			}
			out, stderr, err = runDotForTest(command, "status")
			if err != nil || !strings.Contains(out+stderr, "conflict backups not listed") {
				t.Fatalf("text: %v, %s%s", err, out, stderr)
			}
			if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
				t.Fatalf("status changed conflict symlink: %v", err)
			}
		})
	}
}

// installStaleStoreUnit writes the unit this platform installs for a
// non-default store, still calling the retired `--profile=` argument, and
// returns its path.
func installStaleStoreUnit(t *testing.T, home, store string) string {
	t.Helper()
	files := []struct{ path, body string }{
		{
			path: filepath.Join(home, "Library", "LaunchAgents", "com.dotfiles."+store+".plist"),
			body: "<string>--profile=" + store + "</string>\n",
		},
	}
	if runtime.GOOS != "darwin" {
		files = []struct{ path, body string }{
			{
				path: filepath.Join(home, ".config", "systemd", "user", "dotfiles-"+store+".service"),
				body: "ExecStart=%h/bin/dot sync push --profile=" + store + "\n",
			},
			{
				path: filepath.Join(home, ".config", "systemd", "user", "dotfiles-"+store+".timer"),
				body: "[Timer]\nOnUnitActiveSec=15m\n",
			},
		}
	}
	for _, file := range files {
		writeCLITestFile(t, file.path, file.body)
	}
	return files[0].path
}

// stubSchedulerManager puts a service manager on PATH that reports the job as
// neither running nor loaded and accepts every lifecycle action.
func stubSchedulerManager(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	body := "#!/bin/sh\n[ \"$1\" = print ] && exit 1\nexit 0\n"
	for _, name := range []string{"launchctl", "systemctl"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// A unit installed before #394 keeps invoking `dot sync push --profile=<store>`
// and fails with "unknown flag" on every tick, and nothing rewrites an
// installed unit. `dot sync status` is where an operator looks, so it names the
// unit and the reload pair; `dot sync resume` is the command that re-arms
// units, so it rewrites the argument before reloading.
func TestSyncReportsAUnitInstalledWithTheRetiredStoreFlag(t *testing.T) {
	home, root := goldenSyncFixture(t)
	stubSchedulerManager(t)
	// A custom store is where `dot sync setup --store=<store>` may install a
	// unit; `dot peer setup` owns the peer store's plist instead.
	writeCLITestFile(t, filepath.Join(root, ".dotfiles", "research", "config.yaml"),
		"target: local:"+filepath.Join(home, "gdrive-workspace", "work")+"\npropagation:\n  create: true\n  update: true\n  delete: false\ninterval: 600\n")
	unit := installStaleStoreUnit(t, home, "research")
	before, err := os.ReadFile(unit)
	if err != nil {
		t.Fatal(err)
	}

	out, stderr, err := runDotForTest("sync", "status", "--store=research")
	if err != nil {
		t.Fatalf("status: %v, %s", err, stderr)
	}
	for _, wanted := range []string{"still calls `dot sync --profile=research`", "dot sync --store=research pause && dot sync --store=research resume"} {
		if !strings.Contains(out+stderr, wanted) {
			t.Fatalf("status missing %q: %s%s", wanted, out, stderr)
		}
	}
	if after, err := os.ReadFile(unit); err != nil || string(after) != string(before) {
		t.Fatalf("status rewrote the unit: %v", err)
	}

	out, stderr, err = runDotForTest("sync", "resume", "--store=research")
	if err != nil {
		t.Fatalf("resume: %v, %s", err, stderr)
	}
	if !strings.Contains(out+stderr, "rewrote the installed scheduler unit to call `dot sync --store=research`") {
		t.Fatalf("resume did not report the rewrite: %s%s", out, stderr)
	}
	rewritten, err := os.ReadFile(unit)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rewritten), "--profile=") || !strings.Contains(string(rewritten), "--store=research") {
		t.Fatalf("unit still runs the retired flag:\n%s", rewritten)
	}
	out, stderr, err = runDotForTest("sync", "status", "--store=research")
	if err != nil || strings.Contains(out+stderr, "still calls") {
		t.Fatalf("status kept warning after the rewrite: %v, %s%s", err, out, stderr)
	}
}

func TestSyncStatusReportsSpawnFailed(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("launchd status is macOS-specific")
	}
	home, _ := goldenSyncFixture(t)
	plist := filepath.Join(home, "Library", "LaunchAgents", "com.dotfiles.sync.plist")
	writeCLITestFile(t, plist, "persisted plist")
	dump := filepath.Join(t.TempDir(), "print.out")
	writeCLITestFile(t, dump, "state = spawn failed\nlast exit code = 78: EX_CONFIG\n")
	bin := t.TempDir()
	stub := filepath.Join(bin, "launchctl")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\n[ \"$1\" = print ] || exit 99\n/bin/cat \"$DOTFILES_TEST_LAUNCHCTL_DUMP\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("DOTFILES_TEST_LAUNCHCTL_DUMP", dump)
	out, stderr, err := runDotForTest("sync", "status", "--json")
	if err != nil {
		t.Fatalf("status: %v, %s", err, stderr)
	}
	var document syncStatusJSON
	if err := json.Unmarshal([]byte(out), &document); err != nil {
		t.Fatal(err)
	}
	job := document.Jobs[0]
	if !strings.Contains(job.State, "spawn failed") || job.LastExitCode == nil || *job.LastExitCode != 78 {
		t.Fatalf("job = %+v", job)
	}
	out, stderr, err = runDotForTest("sync", "status")
	for _, wanted := range []string{"spawn failed", "dot sync pause && dot sync resume", "Push last exit", "78"} {
		if err != nil || !strings.Contains(out+stderr, wanted) {
			t.Fatalf("missing %q: %v, %s%s", wanted, err, out, stderr)
		}
	}
	if contents, err := os.ReadFile(plist); err != nil || string(contents) != "persisted plist" {
		t.Fatalf("status changed plist: %v", err)
	}
}
