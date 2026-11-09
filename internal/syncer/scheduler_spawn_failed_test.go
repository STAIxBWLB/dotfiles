package syncer

import (
	"strings"
	"testing"
)

// Fixture reproducing the launchctl fields reported in #233 on the work Mac
// after brew upgrade 2.70.28 → 2.70.29 replaced /opt/homebrew/bin/dot under
// launchd's managed launch constraint (#233): the job stays loaded with
// state = spawn failed and last exit code 78 (EX_CONFIG), and no push runs.
const recordedLaunchdSpawnFailedPrint = `gui/501/com.dotfiles.sync = {
	active count = 0
	copy count = 0
	state = spawn failed
	last exit code = 78: EX_CONFIG
	runs = 42
	pid = 0
}
`

// LaunchdSpawnFailed reads the recorded spawn-failure dump, and only that
// shape: a running job and an ordinary failure exit are still "running".
func TestLaunchdSpawnFailedRecordedOutput(t *testing.T) {
	if !LaunchdSpawnFailed(recordedLaunchdSpawnFailedPrint) {
		t.Error("the recorded spawn-failed dump was not recognized")
	}
	for name, output := range map[string]string{
		"running":        "gui/501/com.dotfiles.sync = {\n\tstate = running\n\tlast exit code = 0\n}\n",
		"ordinary exit":  "gui/501/com.dotfiles.sync = {\n\tstate = not running\n\tlast exit code = 78\n}\n",
		"empty":          "",
		"value prefix":   "state = spawn failed once\n",
		"job state form": "job state = spawn failed\n",
	} {
		got := LaunchdSpawnFailed(output)
		want := name == "job state form"
		if got != want {
			t.Errorf("LaunchdSpawnFailed(%s) = %v, want %v", name, got, want)
		}
	}
}

// The spawn-failed state names the reload command that cleared it on the
// work Mac, so `dot sync status` prints the fix next to the cause (#233).
func TestSchedulerSpawnFailedNamesTheReload(t *testing.T) {
	got := SchedulerSpawnFailed.String()
	if !strings.Contains(got, "spawn failed") || !strings.Contains(got, "dot sync pause && dot sync resume") {
		t.Fatalf("SchedulerSpawnFailed.String() = %q, want the state and the reload command", got)
	}
	if SchedulerSpawnFailed == SchedulerRunning {
		t.Fatal("spawn failed must not read as running")
	}
}

func TestLaunchdLastExitCode(t *testing.T) {
	for _, output := range []string{"last exit code = 78", "\tlast exit code = 78: EX_CONFIG"} {
		if got := LaunchdLastExitCode(output); got == nil || *got != 78 {
			t.Errorf("%q: got %v", output, got)
		}
	}
	for _, output := range []string{"", "last exit code = unavailable", "nested last exit code = 78"} {
		if got := LaunchdLastExitCode(output); got != nil {
			t.Errorf("%q: got %v, want unknown", output, *got)
		}
	}
}

func TestSpawnFailedRecoveryUsesActiveProfile(t *testing.T) {
	got := SchedulerSpawnFailed.StringForProfile("backup")
	if !strings.Contains(got, "dot sync --store='backup' pause && dot sync --store='backup' resume") {
		t.Fatalf("recovery = %q", got)
	}
	if got := SchedulerRunning.StringForProfile("backup"); got != "running" {
		t.Fatalf("running = %q", got)
	}
}
