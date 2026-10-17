package watchdog

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

const pmsetGFixture = `System-wide power settings:
Battery Power:
 standby              1
 sleep                10
 autorestart          0
 womp                 0
 displaysleep         5
AC Power:
 standby              1
 Sleep On Power Button 1
 hibernatefile        /var/vm/sleepimage
 powernap             1
 sleep                1
 autorestart          0
 womp                 0
 hibernatemode        3
 ttyskeepawake        1
 displaysleep         15
`

func TestCapturePower_Fixture(t *testing.T) {
	st, err := CapturePower(pmsetGFixture, "Restart After Freeze: Off")
	if err != nil {
		t.Fatalf("CapturePower: %v", err)
	}
	want := PowerState{
		Sleep: "1", AutoRestart: "0", Womp: "0", RestartFreeze: "off",
		SleepBattery: "10", AutoRestartBattery: "0", WompBattery: "0",
	}
	if st != want {
		t.Fatalf("CapturePower = %#v, want %#v", st, want)
	}
}

func TestCapturePower_RestartFreezeOn(t *testing.T) {
	st, err := CapturePower(pmsetGFixture, "Restart After Power Failure: On\n")
	if err != nil {
		t.Fatalf("CapturePower: %v", err)
	}
	if st.RestartFreeze != "on" {
		t.Fatalf("RestartFreeze = %q", st.RestartFreeze)
	}
}

// pmset annotates a value when something blocks it
// ("sleep 0 (sleep prevented by powerd)"); the annotation must not make the
// value unreadable.
func TestCapturePower_AnnotatedValues(t *testing.T) {
	annotated := ` sleep                0 (sleep prevented by powerd)
 autorestart          1
 womp                 1
`
	st, err := CapturePower(annotated, "Restart After Freeze: On")
	if err != nil {
		t.Fatalf("CapturePower with annotated sleep: %v", err)
	}
	if st.Sleep != "0" || st.AutoRestart != "1" || st.Womp != "1" {
		t.Fatalf("CapturePower = %#v", st)
	}
}

func TestCapturePower_MissingKeysError(t *testing.T) {
	if _, err := CapturePower(" sleep 1\n", "Restart After Freeze: Off"); err == nil {
		t.Fatal("missing autorestart/womp must error")
	}
	if _, err := CapturePower(pmsetGFixture, "gibberish"); err == nil {
		t.Fatal("unparseable restartfreeze must error")
	}
	if _, err := CapturePower(pmsetGFixture, "Restart After Freeze: maybe"); err == nil {
		t.Fatal("unknown restartfreeze value must error")
	}
}

func TestPowerCommandPlans(t *testing.T) {
	apply := PowerApplyCommands()
	wantApply := [][]string{
		{"pmset", "-c", "sleep", "0"},
		{"pmset", "-a", "autorestart", "1"},
		{"pmset", "-a", "womp", "1"},
		{"systemsetup", "-setrestartfreeze", "on"},
	}
	if !reflect.DeepEqual(apply, wantApply) {
		t.Fatalf("apply plan = %v", apply)
	}

	st := PowerState{Sleep: "1", AutoRestart: "0", Womp: "1", RestartFreeze: "off"}
	restore := PowerRestoreCommands(st)
	wantRestore := [][]string{
		{"pmset", "-c", "sleep", "1"},
		{"pmset", "-c", "autorestart", "0"},
		{"pmset", "-c", "womp", "1"},
		{"systemsetup", "-setrestartfreeze", "off"},
	}
	if !reflect.DeepEqual(restore, wantRestore) {
		t.Fatalf("restore plan = %v", restore)
	}

	// A capture with a battery profile restores both profiles, battery
	// commands before the system-wide systemsetup.
	st.SleepBattery, st.AutoRestartBattery, st.WompBattery = "10", "0", "0"
	restore = PowerRestoreCommands(st)
	wantRestore = [][]string{
		{"pmset", "-c", "sleep", "1"},
		{"pmset", "-c", "autorestart", "0"},
		{"pmset", "-c", "womp", "1"},
		{"pmset", "-b", "sleep", "10"},
		{"pmset", "-b", "autorestart", "0"},
		{"pmset", "-b", "womp", "0"},
		{"systemsetup", "-setrestartfreeze", "off"},
	}
	if !reflect.DeepEqual(restore, wantRestore) {
		t.Fatalf("per-source restore plan = %v", restore)
	}
}

// fakeCommandRunner records invocations and can fail a chosen call.
type fakeCommandRunner struct {
	calls  []string
	failAt int // -1 = never
}

func (f *fakeCommandRunner) Run(_ context.Context, name string, args ...string) (*exec.Result, error) {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	if f.failAt == len(f.calls)-1 {
		return &exec.Result{ExitCode: 1}, errors.New("boom")
	}
	return &exec.Result{}, nil
}

func TestRunPowerCommands_SudoWrapsEveryEntry(t *testing.T) {
	r := &fakeCommandRunner{failAt: -1}
	if err := RunPowerCommands(context.Background(), r, true, PowerApplyCommands()); err != nil {
		t.Fatalf("RunPowerCommands: %v", err)
	}
	want := []string{
		"sudo pmset -c sleep 0",
		"sudo pmset -a autorestart 1",
		"sudo pmset -a womp 1",
		"sudo systemsetup -setrestartfreeze on",
	}
	if !reflect.DeepEqual(r.calls, want) {
		t.Fatalf("calls = %v, want %v", r.calls, want)
	}
}

func TestRunPowerCommands_NoSudoAndStopsOnError(t *testing.T) {
	r := &fakeCommandRunner{failAt: 1}
	err := RunPowerCommands(context.Background(), r, false, PowerApplyCommands())
	if err == nil || !strings.Contains(err.Error(), "autorestart") {
		t.Fatalf("error = %v, want it to name the failing command", err)
	}
	if len(r.calls) != 2 {
		t.Fatalf("calls after a failure = %v; later commands must not run", r.calls)
	}
	if r.calls[0] != "pmset -c sleep 0" {
		t.Fatalf("without sudo the plan runs bare: %v", r.calls)
	}
}

func TestPowerState_RoundTrip(t *testing.T) {
	path := t.TempDir() + "/state/power.json"
	in := PowerState{Sleep: "1", AutoRestart: "0", Womp: "1", RestartFreeze: "off"}
	if err := SavePowerState(path, in); err != nil {
		t.Fatalf("SavePowerState: %v", err)
	}
	out, exists, err := LoadPowerState(path)
	if err != nil || !exists {
		t.Fatalf("LoadPowerState = %#v, %v, %v", out, exists, err)
	}
	if out != in {
		t.Fatalf("round trip = %#v, want %#v", out, in)
	}

	// The full arc: capture → save → load → restore plan replays the capture.
	st, err := CapturePower(pmsetGFixture, "Restart After Freeze: On")
	if err != nil {
		t.Fatal(err)
	}
	if err := SavePowerState(path, st); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := LoadPowerState(path)
	if err != nil {
		t.Fatal(err)
	}
	r := &fakeCommandRunner{failAt: -1}
	if err := RunPowerCommands(context.Background(), r, true, PowerRestoreCommands(loaded)); err != nil {
		t.Fatal(err)
	}
	if r.calls[0] != "sudo pmset -c sleep 1" || r.calls[len(r.calls)-1] != "sudo systemsetup -setrestartfreeze on" {
		t.Fatalf("restored calls replay the captured values: %v", r.calls)
	}
	if len(r.calls) != 7 {
		t.Fatalf("a battery-profile capture restores both profiles: %v", r.calls)
	}
}

func TestPowerState_MissingFile(t *testing.T) {
	_, exists, err := LoadPowerState(t.TempDir() + "/absent.json")
	if err != nil || exists {
		t.Fatalf("missing file = exists %v, err %v; want false, nil", exists, err)
	}
}
