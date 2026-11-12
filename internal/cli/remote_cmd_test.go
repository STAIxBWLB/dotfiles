package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
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

// remoteCLIFixture is a sandboxed home with a `remote:` block and a PATH that
// holds only stubs: no real ssh, no real runtime, no real git. What the command
// does off this machine is therefore decided by the stubs alone.
type remoteCLIFixture struct {
	home     string
	checkout string
	// sub is a directory inside the checkout, which is where a run with no PATH
	// argument has to look past to find the tree.
	sub string
}

func newRemoteCLIFixture(t *testing.T) remoteCLIFixture {
	t.Helper()
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	checkout := filepath.Join(home, "workspace", "repo")
	sub := filepath.Join(checkout, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	stubs := map[string]string{
		// ssh answers setup's check, narrates a pull the way a runtime does, and
		// reports the image's user.
		"ssh": "#!/bin/sh\n" +
			"case \"$*\" in\n" +
			"  *\"image inspect\"*) echo 1000:1000 ;;\n" +
			"  *\"pull \"*) echo 'Step 1/4 : FROM debian:12-slim'; echo 'Step 2/4 : RUN true' ;;\n" +
			"  *) printf 'git=/usr/bin/git\\nrsync=/usr/bin/rsync\\nrsync_version=rsync  version 3.2.7  protocol version 31\\nruntime=Docker version 27.1.1, build 6312585\\nuser=builder\\ncpus=12\\n' ;;\n" +
			"esac\n",
		"rsync": "#!/bin/sh\necho 'rsync  version 3.2.7  protocol version 31'\n",
		"ps":    "#!/bin/sh\necho 'Mon Jan  1 00:00:00 2024'\n",
		// git names whichever checkout a run asks about, so the run resolves the
		// tree without a real repository.
		"git": "#!/bin/sh\ncase \"$*\" in\n  *--show-toplevel*) echo '__CHECKOUT__' ;;\n  *) printf 'docs/a.md\\000' ;;\nesac\n",
	}
	for name, body := range stubs {
		body = strings.ReplaceAll(body, "__CHECKOUT__", checkout)
		if err := os.WriteFile(filepath.Join(bin, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	return remoteCLIFixture{home: home, checkout: checkout, sub: sub}
}

// writeConfig writes the sandbox's config file, which is what decides the host
// a command addresses.
func (f remoteCLIFixture) writeConfig(t *testing.T, body string) {
	t.Helper()
	writeCLITestFile(t, filepath.Join(f.home, ".config", "dotfiles", "config.yaml"), body)
}

// --config names a sync config, and the remote block lives in the user state, so
// `dot remote` refuses the flag instead of addressing the default state's host
// while the person reads another file. The fixture's state and the named file
// name different hosts, which is what makes a silent ignore visible here.
func TestRemoteRefusesTheConfigFlag(t *testing.T) {
	f := newRemoteCLIFixture(t)
	f.writeConfig(t, "remote:\n  default: buildbox\n  hosts:\n    buildbox:\n      image:\n        ref: example.invalid/dev:1\n")
	other := filepath.Join(f.home, "other.yaml")
	writeCLITestFile(t, other, "remote:\n  default: elsewhere\n  hosts:\n    elsewhere:\n      image:\n        ref: example.invalid/dev:2\n")
	for _, args := range [][]string{
		{"remote", "setup", "--config", other},
		{"remote", "status", "--config", other},
	} {
		_, _, err := runDotForTest(args...)
		if err == nil {
			t.Fatalf("dot %s succeeded, want a refusal naming --config", strings.Join(args, " "))
		}
		for _, want := range []string{"--config", "user state", "--home"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("dot %s = %v, want it to carry %q", strings.Join(args, " "), err, want)
			}
		}
	}
	// Without the flag the state's own host is used, so the refusal does not
	// block ordinary use.
	if _, errOut, err := runDotForTest("remote", "status"); err != nil {
		t.Fatalf("remote status without --config: %v\nstderr:\n%s", err, errOut)
	}
}

// `setup --json` has to be one document on stdout even when the runtime
// narrates the pull: a wrapper that parses the report cannot skip past a
// progress line, and the whole stdout is what it decodes.
func TestRemoteSetupJSONIsOneDocumentWhileTheRuntimeNarrates(t *testing.T) {
	f := newRemoteCLIFixture(t)
	f.writeConfig(t, "remote:\n  default: buildbox\n  hosts:\n    buildbox:\n      image:\n        ref: example.invalid/dev:1\n")
	out, errOut, err := runDotForTest("remote", "setup", "--json")
	if err != nil {
		t.Fatalf("remote setup --json: %v\nstderr:\n%s", err, errOut)
	}
	var report remote.SetupReport
	if uerr := json.Unmarshal([]byte(out), &report); uerr != nil {
		t.Fatalf("stdout is not one JSON document (%v):\n%s", uerr, out)
	}
	if report.Host != "buildbox" || report.ImageUser != "1000:1000" {
		t.Errorf("report = %+v, want the host and the image's unprivileged user", report)
	}
	if !strings.Contains(errOut, "Step 1/4") {
		t.Errorf("the runtime's progress is not on stderr:\n%s", errOut)
	}
}

// With no PATH argument the checkout is the one the current directory belongs
// to, and a project entry's host applies to it. Choosing the host before
// resolving that checkout would only ever see remote.default, which this
// fixture deliberately does not set.
func TestRemoteRunTakesTheHostFromTheProjectEntryOfTheDefaultCheckout(t *testing.T) {
	f := newRemoteCLIFixture(t)
	f.writeConfig(t, "remote:\n"+
		"  local_root: "+filepath.Join(f.home, "workspace")+"\n"+
		"  hosts:\n"+
		"    projbox:\n"+
		"      ssh: projbox-alias\n"+
		"      image:\n"+
		"        ref: example.invalid/dev:1\n"+
		"  projects:\n"+
		"    "+f.checkout+":\n"+
		"      host: projbox\n")
	t.Chdir(f.sub)
	out, errOut, err := runDotForTest("remote", "run", "--dry-run", "--", "true")
	if err != nil {
		t.Fatalf("remote run --dry-run: %v\nstderr:\n%s", err, errOut)
	}
	if !strings.Contains(out, "projbox-alias:"+remote.DefaultRoot+"/repo") {
		t.Errorf("the dry run did not target the project's host:\n%s", out)
	}
}
