package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/admission"
	"github.com/STAIxBWLB/dotfiles/internal/remote"
)

// parseRemoteRunArgs runs the real cobra parse for `dot remote run` and returns
// what splitRemoteArgs made of it.
func parseRemoteRunArgs(t *testing.T, args []string) (string, []string, error) {
	t.Helper()
	root := newRemoteCmd()
	run, _, err := root.Find([]string{"run"})
	if err != nil {
		t.Fatalf("find run: %v", err)
	}
	var path string
	var command []string
	var splitErr error
	run.RunE = func(cmd *cobra.Command, _ []string) error {
		path, command, splitErr = splitRemoteArgs(cmd)
		return nil
	}
	root.SetArgs(append([]string{"run"}, args...))
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	return path, command, splitErr
}

func TestSplitRemoteArgs(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		path    string
		command []string
		wantErr string
	}{
		{name: "no path", args: []string{"--", "make", "test"}, command: []string{"make", "test"}},
		{name: "path and argv", args: []string{"/tmp/one", "--", "make", "test"}, path: "/tmp/one", command: []string{"make", "test"}},
		{name: "one script argument", args: []string{"--", "make test && echo ok"}, command: []string{"make test && echo ok"}},
		// Without a separator the whole list is the command: a bare first
		// argument is far more often a command than a checkout.
		{name: "no separator", args: []string{"make", "test"}, command: []string{"make", "test"}},
		{name: "no command", args: []string{}, wantErr: "needs a command"},
		{name: "two paths", args: []string{"a", "b", "--", "make"}, wantErr: "at most one PATH"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path, command, err := parseRemoteRunArgs(t, tc.args)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("accepted %v", tc.args)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error %q does not name %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("split: %v", err)
			}
			if path != tc.path {
				t.Errorf("path = %q, want %q", path, tc.path)
			}
			if strings.Join(command, "\x00") != strings.Join(tc.command, "\x00") {
				t.Errorf("command = %v, want %v", command, tc.command)
			}
		})
	}
}

func deferFixture() *remote.Deferred {
	return &remote.Deferred{Outcome: admission.DeferOutcome{
		Outcome:           "deferred",
		Scope:             "/home/u/workspace/one",
		Class:             admission.ClassRemote,
		Reason:            "another run of /home/u/workspace/one is active since 2026-01-01T10:00:00Z (me@mac, pid 42)",
		RetryAfterSeconds: 2,
	}}
}

// A deferred run prints the same record `dot admit` prints and exits 75.
func TestRemoteDeferExitPrintsTheRecordAndReturnsSeventyFive(t *testing.T) {
	var out, errOut bytes.Buffer
	p := &Printer{Out: &out, Err: &errOut}
	err := remoteDeferExit(p, deferFixture(), false)
	var exitErr *ExitCodeError
	if !errors.As(err, &exitErr) || exitErr.Code != ExitDeferred {
		t.Fatalf("err = %v, want ExitCodeError %d", err, ExitDeferred)
	}
	if ExitDeferred != 75 {
		t.Errorf("ExitDeferred = %d, want 75 (EX_TEMPFAIL)", ExitDeferred)
	}
	if !strings.Contains(errOut.String(), "remote run deferred (scope /home/u/workspace/one)") {
		t.Errorf("stderr = %q", errOut.String())
	}
	for _, want := range []string{"another run of /home/u/workspace/one", "2s"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("stdout does not carry %q: %q", want, out.String())
		}
	}
	if !strings.Contains(err.Error(), "remote run deferred: another run of") {
		t.Errorf("error = %q", err)
	}
}

func TestRemoteDeferExitPrintsJSON(t *testing.T) {
	var out, errOut bytes.Buffer
	p := &Printer{Out: &out, Err: &errOut}
	err := remoteDeferExit(p, deferFixture(), true)
	var exitErr *ExitCodeError
	if !errors.As(err, &exitErr) || exitErr.Code != ExitDeferred {
		t.Fatalf("err = %v, want exit 75", err)
	}
	var record map[string]any
	if uerr := json.Unmarshal(out.Bytes(), &record); uerr != nil {
		t.Fatalf("stdout is not one JSON record: %v\n%s", uerr, out.String())
	}
	if record["outcome"] != "deferred" || record["class"] != "remote" {
		t.Errorf("record = %v", record)
	}
	if record["retry_after"] != float64(2) {
		t.Errorf("retry_after = %v, want 2", record["retry_after"])
	}
	if !strings.Contains(record["reason"].(string), "another run of") {
		t.Errorf("reason = %v", record["reason"])
	}
}

func TestStatusImageLine(t *testing.T) {
	cases := map[string]struct {
		status remote.Status
		want   string
	}{
		"age": {
			status: remote.Status{Image: "me-rdev:latest", ImageAge: "48h0m0s"},
			want:   "me-rdev:latest (built 48h0m0s ago)",
		},
		"created only": {
			status: remote.Status{Image: "me-rdev:latest", ImageCreated: "2026-01-01T10:00:00Z"},
			want:   "me-rdev:latest (built 2026-01-01T10:00:00Z)",
		},
		"missing": {
			status: remote.Status{Image: "me-rdev:latest"},
			want:   "me-rdev:latest (the host did not report it; run `dot remote setup`)",
		},
	}
	for name, tc := range cases {
		if got := statusImageLine(tc.status); got != tc.want {
			t.Errorf("%s: statusImageLine = %q, want %q", name, got, tc.want)
		}
	}
}

func TestCountRunsAndTotalSize(t *testing.T) {
	runs := []remote.ActiveRun{
		{Kind: "path", Scope: "/a"},
		{Kind: "host", Scope: "remote-host:buildbox"},
		{Kind: "path", Scope: "/b"},
	}
	if got := countRuns(runs, "path"); got != 2 {
		t.Errorf("path runs = %d, want 2", got)
	}
	if got := countRuns(runs, "host"); got != 1 {
		t.Errorf("host runs = %d, want 1", got)
	}
	if got := countRuns(nil, "path"); got != 0 {
		t.Errorf("no runs = %d", got)
	}
	dirs := []remote.DirSize{{Bytes: 1024 * 1024}, {Bytes: 1024 * 1024}}
	if got := totalSize(dirs); got != "2.0 MiB" {
		t.Errorf("totalSize = %q, want 2.0 MiB", got)
	}
	if got := totalSize(nil); got != "0 B" {
		t.Errorf("totalSize of nothing = %q", got)
	}
}

func TestSizeLabel(t *testing.T) {
	cases := map[int64]string{
		0:                  "0 B",
		512:                "512 B",
		1024:               "1.0 KiB",
		1024 * 1024:        "1.0 MiB",
		3 * 1024 * 1024:    "3.0 MiB",
		1024 * 1024 * 1024: "1.0 GiB",
	}
	for bytes, want := range cases {
		if got := sizeLabel(bytes); got != want {
			t.Errorf("sizeLabel(%d) = %q, want %q", bytes, got, want)
		}
	}
}

// The rules a remote run steps past come from the environment and the relax
// override only, so a broken unrelated admission key cannot block remote work.
func TestRemoteIgnoreReadsTheEnvironmentAndNothingElse(t *testing.T) {
	t.Setenv(admission.IgnoreEnv, "remote-load")
	set, source, err := remoteIgnore(nil)
	if err != nil {
		t.Fatalf("remoteIgnore: %v", err)
	}
	if !set.RemoteLoad {
		t.Error("the environment did not relax the load check")
	}
	if source != admission.IgnoreEnv {
		t.Errorf("source = %q, want %q", source, admission.IgnoreEnv)
	}

	t.Setenv(admission.IgnoreEnv, "all,slots")
	if _, _, err := remoteIgnore(nil); err == nil {
		t.Error("an unknown rule was accepted")
	} else if !strings.Contains(err.Error(), admission.IgnoreEnv) {
		t.Errorf("error %q does not name the variable", err)
	}

	t.Setenv(admission.IgnoreEnv, "")
	set, source, err = remoteIgnore(nil)
	if err != nil {
		t.Fatalf("remoteIgnore: %v", err)
	}
	if set.Any() || source != "" {
		t.Errorf("set = %+v from %q, want nothing", set, source)
	}
}
