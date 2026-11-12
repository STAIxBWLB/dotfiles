package remote

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const setupReply = "git=/usr/bin/git\n" +
	"rsync=/usr/bin/rsync\n" +
	"rsync_version=rsync  version 3.2.7  protocol version 31\n" +
	"runtime=Docker version 27.1.1, build 6312585\n" +
	"user=builder\n" +
	"cpus=12\n"

func setupDeps(t *testing.T, reply string) (Deps, *fakeExec, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	fake := &fakeExec{t: t, output: reply}
	var out, errOut bytes.Buffer
	return Deps{Exec: fake.execute, Out: &out, Err: &errOut}, fake, &out, &errOut
}

func TestSetupVerifiesTheHostAndPullsAPrebuiltImage(t *testing.T) {
	deps, fake, _, errOut := setupDeps(t, setupReply)
	host := testHost()
	report, err := deps.Setup(context.Background(), host, SetupOptions{})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if report.User != "builder" || report.CPUs != 12 {
		t.Errorf("report = %+v, want the host's own answers", report)
	}
	if report.Built {
		t.Error("a ref host reported a built image")
	}
	if report.RuntimeVersion != "Docker version 27.1.1, build 6312585" {
		t.Errorf("runtime version = %q", report.RuntimeVersion)
	}
	if got := fake.calls[0][len(fake.calls[0])-1]; !strings.Contains(got, "printf 'cpus=%s") {
		t.Errorf("the check script does not print the CPU count:\n%s", got)
	}
	if got := fake.calls[0][len(fake.calls[0])-1]; !strings.Contains(got, "docker --version") {
		t.Errorf("the check script does not ask the configured runtime:\n%s", got)
	}
	pull := fake.calls[1][len(fake.calls[1])-1]
	if !strings.Contains(pull, "docker pull 'ghcr.io/org/dev:1'") {
		t.Errorf("pull command = %q", pull)
	}
	if !strings.Contains(errOut.String(), "dot remote: pulling ghcr.io/org/dev:1 on buildbox") {
		t.Errorf("stderr does not say what setup is doing:\n%s", errOut.String())
	}
}

func TestSetupBuildsTheDockerfileOverStdin(t *testing.T) {
	deps, fake, _, errOut := setupDeps(t, setupReply)
	host := testHost()
	host.Ref = ""
	host.Dockerfile = filepath.Join(t.TempDir(), "Dockerfile.dev")
	dockerfile := "FROM ubuntu:24.04\nUSER ubuntu\n"
	if err := os.WriteFile(host.Dockerfile, []byte(dockerfile), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err := deps.Setup(context.Background(), host, SetupOptions{})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if !report.Built {
		t.Error("a Dockerfile host did not report a build")
	}
	if got := report.Image; got != host.Prefix+":latest" {
		t.Errorf("image = %q, want the built tag", got)
	}
	build := fake.calls[1][len(fake.calls[1])-1]
	want := "docker build -t '" + host.Prefix + ":latest' --build-arg UID=$(id -u) --build-arg GID=$(id -g) -"
	if build != want {
		t.Errorf("build command = %q, want %q", build, want)
	}
	if got := string(fake.stin(fake.calls[1])); got != dockerfile {
		t.Errorf("build sent %q on stdin, want the Dockerfile", got)
	}
	if !strings.Contains(errOut.String(), "building "+host.Prefix+":latest on buildbox from "+host.Dockerfile) {
		t.Errorf("stderr does not say what setup is building:\n%s", errOut.String())
	}
}

func TestSetupNamesEveryGapInTheHost(t *testing.T) {
	cases := []struct {
		name  string
		reply string
		want  string
	}{
		{"no git", "git=missing\nrsync=/usr/bin/rsync\nrsync_version=rsync  version 3.2.7  protocol version 31\nruntime=Docker 27\n",
			"has no git on PATH"},
		{"no rsync", "git=/usr/bin/git\nrsync=missing\nruntime=Docker 27\n",
			"has no rsync on PATH"},
		{"old rsync", "git=/usr/bin/git\nrsync=/usr/bin/rsync\nrsync_version=rsync  version 3.2.1  protocol version 31\nruntime=Docker 27\n",
			"too old"},
		{"openrsync", "git=/usr/bin/git\nrsync=/usr/bin/rsync\nrsync_version=openrsync: protocol version 29\nruntime=Docker 27\n",
			"openrsync"},
		{"no runtime", "git=/usr/bin/git\nrsync=/usr/bin/rsync\nrsync_version=rsync  version 3.2.7  protocol version 31\nruntime=\n",
			"has no docker on PATH"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps, fake, _, _ := setupDeps(t, tc.reply)
			_, err := deps.Setup(context.Background(), testHost(), SetupOptions{})
			if err == nil {
				t.Fatalf("Setup accepted a host with %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not name %q", err, tc.want)
			}
			if !strings.Contains(err.Error(), "buildbox") {
				t.Errorf("error %q does not name the host", err)
			}
			// Nothing was built or pulled after the check failed.
			if len(fake.calls) != 1 {
				t.Errorf("%d commands sent, want only the check", len(fake.calls))
			}
		})
	}
}

func TestSetupDryRunSendsNothing(t *testing.T) {
	deps, fake, _, errOut := setupDeps(t, "")
	host := testHost()
	report, err := deps.Setup(context.Background(), host, SetupOptions{DryRun: true})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if len(fake.calls) != 0 {
		t.Errorf("a dry run sent %d commands", len(fake.calls))
	}
	// The plan goes to stderr so stdout stays one document for --json.
	stderr := errOut.String()
	if !strings.Contains(stderr, "would check buildbox over ssh") {
		t.Errorf("stderr does not describe the check:\n%s", stderr)
	}
	if !strings.Contains(stderr, "would pull ghcr.io/org/dev:1 on buildbox") {
		t.Errorf("stderr does not describe the image step:\n%s", stderr)
	}
	if report.Image != host.Image() {
		t.Errorf("report image = %q", report.Image)
	}
}

func TestParseKeyValues(t *testing.T) {
	values := parseKeyValues("git=/usr/bin/git\n  rsync_version = rsync  version 3.2.7  protocol version 31 \nnoise\n\nruntime=\n")
	if values["git"] != "/usr/bin/git" {
		t.Errorf("git = %q", values["git"])
	}
	if values["rsync_version"] != "rsync  version 3.2.7  protocol version 31" {
		t.Errorf("rsync_version = %q, want it trimmed", values["rsync_version"])
	}
	if _, ok := values["noise"]; ok {
		t.Error("a line without a key was recorded")
	}
	if got, ok := values["runtime"]; !ok || got != "" {
		t.Errorf("runtime = %q, %v, want an empty value recorded", got, ok)
	}
}

// A report a wrapper parses cannot share stdout with the runtime's own
// progress: docker and podman both narrate a build and a pull.
func TestSetupJSONKeepsStdoutForTheReport(t *testing.T) {
	deps, fake, out, errOut := setupDeps(t, setupReply)
	fake.progress = "Step 1/4 : FROM debian:12-slim\nStep 2/4 : RUN apt-get update\n"
	report, err := deps.Setup(context.Background(), testHost(), SetupOptions{JSON: true})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if got := out.String(); got != "" {
		t.Errorf("stdout carries %q, want nothing but the report", got)
	}
	for _, want := range []string{"Step 1/4", "dot remote: pulling ghcr.io/org/dev:1 on buildbox"} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("stderr does not carry %q:\n%s", want, errOut.String())
		}
	}
	if report.ImageUser == "" {
		t.Error("the report lost the image user")
	}
}

// Without --json the runtime's progress stays where a person watching the
// command expects it: stdout, in order with the rest of what setup says.
func TestSetupWithoutJSONStreamsTheRuntimeToStdout(t *testing.T) {
	deps, fake, out, _ := setupDeps(t, setupReply)
	fake.progress = "Step 1/4 : FROM debian:12-slim\n"
	if _, err := deps.Setup(context.Background(), testHost(), SetupOptions{}); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if !strings.Contains(out.String(), "Step 1/4") {
		t.Errorf("stdout does not carry the runtime's progress:\n%s", out.String())
	}
}

// The image's own user is the one a run gets, so setup refuses an image whose
// user is root: everything that image writes into the mirror would belong to
// root, and the host user could not clean it up.
func TestSetupRefusesAnImageWhoseUserIsRoot(t *testing.T) {
	cases := map[string]string{"no USER": "0", "root": "root", "uid 0 with a group": "0:0"}
	for name, user := range cases {
		t.Run(name, func(t *testing.T) {
			deps, fake, _, _ := setupDeps(t, setupReply)
			fake.imageUser = user
			_, err := deps.Setup(context.Background(), testHost(), SetupOptions{})
			if err == nil {
				t.Fatalf("setup accepted an image with %s (user %q)", name, user)
			}
			for _, want := range []string{"without an unprivileged USER", "USER dev", "dot remote setup"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not name %q", err, want)
				}
			}
		})
	}
}

// An image that could not be inspected names the next step rather than
// reporting an unnamed failure.
func TestSetupNamesAnUnreadableImage(t *testing.T) {
	deps, fake, _, _ := setupDeps(t, setupReply)
	fake.imageErr = errors.New("no such image")
	_, err := deps.Setup(context.Background(), testHost(), SetupOptions{})
	if err == nil {
		t.Fatal("setup accepted an image it could not inspect")
	}
	if !strings.Contains(err.Error(), "dot remote setup") {
		t.Errorf("error %q does not name the next step", err)
	}
}

// A report carries the user a run will be, which is what the human half of
// setup prints.
func TestSetupReportsTheImageUser(t *testing.T) {
	deps, fake, _, _ := setupDeps(t, setupReply)
	fake.imageUser = "1000:1000"
	report, err := deps.Setup(context.Background(), testHost(), SetupOptions{})
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if report.ImageUser != "1000:1000" {
		t.Errorf("image user = %q", report.ImageUser)
	}
}
