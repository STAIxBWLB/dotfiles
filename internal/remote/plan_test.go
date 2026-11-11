package remote

import (
	"slices"
	"strings"
	"testing"
)

func testHost() Host {
	return Host{
		Name:    "buildbox",
		SSH:     "buildbox",
		Runtime: "docker",
		Prefix:  "me-rdev",
		Root:    DefaultRoot,
		GitRoot: DefaultGitRoot,
		SrcRoot: DefaultSrcRoot,
		CPUs:    16,
		Memory:  DefaultMemory,
		MaxRuns: 3,
		Ref:     "ghcr.io/org/dev:1",
	}
}

func TestSSHArgvIsBatchMode(t *testing.T) {
	want := []string{"ssh", "-o", "BatchMode=yes", "buildbox", "printf hi"}
	if got := SSHArgv("buildbox", "printf hi"); !slices.Equal(got, want) {
		t.Errorf("SSHArgv = %v, want %v", got, want)
	}
}

func TestSyncPlanForAGitCheckout(t *testing.T) {
	listing := []byte("main.go\x00docs/a.md\x00")
	steps := SyncPlan("/usr/local/bin/rsync", "/home/u/repo", "buildbox:dot-remote/repo",
		[]string{".git", "node_modules"}, listing)
	if len(steps) != 2 {
		t.Fatalf("steps = %d, want the ship pass and the delete pass", len(steps))
	}
	ship, prune := steps[0], steps[1]
	for _, want := range []string{"-a", "--mkpath", "--files-from=-", "--from0", "--ignore-missing-args",
		"--exclude=.git", "--exclude=node_modules", "--include=.env.example",
		"/home/u/repo/", "buildbox:dot-remote/repo/"} {
		if !slices.Contains(ship.Argv, want) {
			t.Errorf("ship pass is missing %q: %v", want, ship.Argv)
		}
	}
	if string(ship.Stdin) != string(listing) {
		t.Errorf("ship stdin = %q, want git's listing", ship.Stdin)
	}
	if ship.Argv[0] != "/usr/local/bin/rsync" {
		t.Errorf("ship argv starts with %q, want the resolved rsync client", ship.Argv[0])
	}
	// The template include has to precede the excludes, or .env.* would drop it.
	include, exclude := -1, -1
	for i, arg := range ship.Argv {
		if arg == "--include="+envTemplateInclude && include < 0 {
			include = i
		}
		if strings.HasPrefix(arg, "--exclude=") && exclude < 0 {
			exclude = i
		}
	}
	if include < 0 || exclude < 0 || include > exclude {
		t.Errorf("include at %d, first exclude at %d: the template must come first", include, exclude)
	}
	for _, want := range []string{"--delete", "--filter=:- .gitignore", "--existing", "--ignore-existing"} {
		if !slices.Contains(prune.Argv, want) {
			t.Errorf("delete pass is missing %q: %v", want, prune.Argv)
		}
	}
	if prune.Stdin != nil {
		t.Error("the delete pass must transfer nothing, but it was given stdin")
	}
	if slices.Contains(prune.Argv, "--files-from=-") {
		t.Error("the delete pass listed files instead of pruning")
	}
}

func TestSyncPlanForAPathThatIsNotAGitCheckout(t *testing.T) {
	steps := SyncPlan("rsync", "/home/u/plain", "buildbox:dot-remote/plain", []string{".git"}, nil)
	if len(steps) != 1 {
		t.Fatalf("steps = %d, want one plain mirroring pass", len(steps))
	}
	argv := steps[0].Argv
	if !slices.Contains(argv, "--delete") || slices.Contains(argv, "--files-from=-") {
		t.Errorf("plain pass = %v, want --delete and no file list", argv)
	}
	if steps[0].Stdin != nil {
		t.Error("a plain mirroring pass takes no stdin")
	}
}

func TestContainerArgv(t *testing.T) {
	host := testHost()
	host.Caches = []Cache{{Name: "cargo", Target: "/usr/local/cargo/registry"}}
	host.Env = []EnvPair{{Key: "LANG", Value: "C.UTF-8"}}
	argv := ContainerArgv(host, "me-rdev-0123456789ab", "work/dev/one", []string{"make", "test"})
	joined := strings.Join(argv, " ")
	for _, want := range []string{
		"run --rm --init -i --name me-rdev-0123456789ab",
		"--cpus 16",
		"--memory 32g",
		"-w /work/work/dev/one",
		"-v me-rdev-cargo:/usr/local/cargo/registry",
		"-e CARGO_BUILD_JOBS=16",
		"-e GOMAXPROCS=16",
		"-e LANG=C.UTF-8",
		"ghcr.io/org/dev:1 bash -c make test",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("container argv is missing %q:\n%s", want, joined)
		}
	}
	// An argv becomes one shell script, which is what the container's bash -c
	// receives; RunCommand quotes it as a single word.
	if script := argv[len(argv)-1]; script != "make test" {
		t.Errorf("script = %q, want the joined argv", script)
	}
	// The local user is restored to, and the mirror mount is added on the host
	// side only, so it must not appear here.
	if strings.Contains(joined, "--user") {
		t.Errorf("container argv sets --user, but the image's own user runs the job:\n%s", joined)
	}
	if strings.Contains(joined, "$HOME") {
		t.Errorf("container argv expanded the host home:\n%s", joined)
	}
	// A script is passed through untouched, which is the one-argument form.
	script := ContainerArgv(host, "c", "work/one", []string{"make test && echo ok"})
	if last := script[len(script)-1]; last != "make test && echo ok" {
		t.Errorf("script = %q, want the single argument unquoted", last)
	}
}

func TestContainerArgvOmitsCPUsWhenUnset(t *testing.T) {
	host := testHost()
	host.CPUs = 0
	joined := strings.Join(ContainerArgv(host, "c", "one", []string{"true"}), " ")
	if strings.Contains(joined, "--cpus") {
		t.Errorf("argv names --cpus with no cpus configured:\n%s", joined)
	}
	if strings.Contains(joined, "GOMAXPROCS") {
		t.Errorf("argv derives GOMAXPROCS with no cpus configured:\n%s", joined)
	}
}

func TestRunCommandMountsTheMirrorWithTheHostHome(t *testing.T) {
	host := testHost()
	got := RunCommand(host, "me-rdev-0123456789ab", "work/dev/one", []string{"go", "test", "-p", "2", "./..."})
	if !strings.HasPrefix(got, "docker run --rm ") {
		t.Errorf("command does not start the runtime with --rm:\n%s", got)
	}
	if !strings.Contains(got, `-v "$HOME"/'dot-remote/work/dev/one':'/work/work/dev/one'`) {
		t.Errorf("the mirror mount is not $HOME-relative and quoted:\n%s", got)
	}
	// Several arguments arrive as one script, so the shell parses the argv back
	// into the words the person typed.
	if !strings.HasSuffix(got, "bash -c 'go test -p 2 ./...'") {
		t.Errorf("argv was not passed as one quoted script:\n%s", got)
	}
	if strings.Count(got, "-v ") != 1 {
		t.Errorf("expected exactly the mirror mount, got:\n%s", got)
	}
}

func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"":            "''",
		"plain":       "'plain'",
		"it's":        `'it'\''s'`,
		"a b":         "'a b'",
		`$HOME/"x"`:   `'$HOME/"x"'`,
		"tab\tand nl": "'tab\tand nl'",
	}
	for word, want := range cases {
		if got := ShellQuote(word); got != want {
			t.Errorf("ShellQuote(%q) = %s, want %s", word, got, want)
		}
	}
}

func TestCommandScript(t *testing.T) {
	if got := CommandScript([]string{"go test ./..."}); got != "go test ./..." {
		t.Errorf("one argument = %q, want the script as written", got)
	}
	if got := CommandScript([]string{"go", "test", "./..."}); got != "go test ./..." {
		t.Errorf("several arguments = %q, want the argv joined into a script", got)
	}
	if got := CommandScript([]string{"sh", "-c", "echo 'hi there'"}); got != `sh -c 'echo '\''hi there'\'''` {
		t.Errorf("several arguments = %q, want each word quoted for the shell", got)
	}
}

func TestCheckRsyncVersion(t *testing.T) {
	cases := []struct {
		banner  string
		want    string
		wantErr string
	}{
		{banner: "rsync  version 3.4.1  protocol version 32", want: "3.4.1"},
		{banner: "rsync  version 3.2.3  protocol version 31", want: "3.2.3"},
		{banner: "rsync  version 3.2.1  protocol version 31", wantErr: "too old"},
		{banner: "rsync  version 2.6.9  protocol version 29", wantErr: "too old"},
		{banner: "openrsync: protocol version 29\nopenrsync 0.5", wantErr: "openrsync"},
		{banner: "not a version at all", wantErr: "cannot read a version"},
	}
	for _, tc := range cases {
		got, err := CheckRsyncVersion(tc.banner)
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("CheckRsyncVersion(%q) err = %v, want %q", tc.banner, err, tc.wantErr)
			}
			continue
		}
		if err != nil {
			t.Errorf("CheckRsyncVersion(%q) = %v", tc.banner, err)
			continue
		}
		if got != tc.want {
			t.Errorf("version = %q, want %q", got, tc.want)
		}
	}
}

func TestGitListArgsIncludeUntrackedNotIgnored(t *testing.T) {
	want := []string{"ls-files", "-z", "--cached", "--others", "--exclude-standard"}
	if got := gitListArgs(); !slices.Equal(got, want) {
		t.Errorf("gitListArgs = %v, want %v", got, want)
	}
}
