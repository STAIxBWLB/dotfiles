package gittransport

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestClassifySSH(t *testing.T) {
	cases := []struct {
		name, out string
		usable    bool
	}{
		{"authenticated", "Hi octocat! You've successfully authenticated, but GitHub does not provide shell access.\n", true},
		{"key rejected", "git@github.com: Permission denied (publickey).\n", false},
		{"reset after ident", "kex_exchange_identification: read: Connection reset by peer\nConnection reset by 20.200.245.247 port 22\n", false},
		{"timeout", "ssh: connect to host github.com port 22: Operation timed out\n", false},
		{"dns", "ssh: Could not resolve hostname github.com: nodename nor servname provided, or not known\n", false},
		{"empty", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := ClassifySSH(c.out)
			if r.Usable != c.usable || r.Detail == "" {
				t.Fatalf("ClassifySSH(%q) = %+v, want usable=%v with a detail", c.out, r, c.usable)
			}
		})
	}
}

func TestDecide(t *testing.T) {
	ok, bad := Result{Usable: true}, Result{}
	cases := []struct {
		name       string
		current    Mode
		ssh, https Result
		want       Mode
		change     bool
	}{
		{"ssh usable keeps ssh", ModeSSH, ok, ok, ModeSSH, false},
		{"ssh recovers", ModeHTTPS, ok, ok, ModeSSH, true},
		{"ssh blocked switches", ModeSSH, bad, ok, ModeHTTPS, true},
		{"https already", ModeHTTPS, bad, ok, ModeHTTPS, false},
		{"both broken keeps https", ModeHTTPS, bad, bad, ModeHTTPS, false},
		{"both broken keeps ssh", ModeSSH, bad, bad, ModeSSH, false},
		{"missing file defaults to ssh", "", bad, bad, ModeSSH, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, change := Decide(c.current, c.ssh, c.https)
			if got != c.want || change != c.change {
				t.Fatalf("Decide = (%s, %v), want (%s, %v)", got, change, c.want, c.change)
			}
		})
	}
}

func TestWriteIsIdempotentAndCurrentRoundTrips(t *testing.T) {
	home := t.TempDir()
	if m, err := Current(home); err != nil || m != "" {
		t.Fatalf("Current on empty home = (%q, %v)", m, err)
	}
	for _, m := range []Mode{ModeHTTPS, ModeSSH} {
		changed, err := Write(home, m)
		if err != nil || !changed {
			t.Fatalf("Write(%s) = (%v, %v), want a change", m, changed, err)
		}
		if changed, err = Write(home, m); err != nil || changed {
			t.Fatalf("second Write(%s) = (%v, %v), want no change", m, changed, err)
		}
		if got, _ := Current(home); got != m {
			t.Fatalf("Current = %q, want %q", got, m)
		}
	}
	if err := os.WriteFile(Path(home), []byte("[url \"x\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, _ := Current(home); got != "" {
		t.Fatalf("Current on foreign content = %q, want empty", got)
	}
}

// Each mode block must send both GitHub URL forms to its transport when git
// resolves them. No network: ls-remote --get-url only expands the URL.
func TestBlocksRewriteBothURLForms(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	cases := map[Mode]string{ModeSSH: "git@github.com:o/r.git", ModeHTTPS: "https://github.com/o/r.git"}
	for mode, want := range cases {
		home := t.TempDir()
		if _, err := Write(home, mode); err != nil {
			t.Fatal(err)
		}
		for _, url := range []string{"git@github.com:o/r.git", "https://github.com/o/r.git", "ssh://git@github.com/o/r.git"} {
			if mode == ModeSSH && strings.HasPrefix(url, "ssh://") {
				continue // ssh mode leaves explicit ssh:// URLs as they are
			}
			cmd := exec.Command("git", "-c", "include.path="+Path(home), "ls-remote", "--get-url", url)
			cmd.Dir = home
			cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+filepath.Join(home, "none"), "HOME="+home)
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("%s %s: %v", mode, url, err)
			}
			if got := strings.TrimSpace(string(out)); got != want {
				t.Errorf("%s mode resolves %s to %s, want %s", mode, url, got, want)
			}
		}
	}
}

func TestRunWritesOnlyOnChangeAndHonorsCheck(t *testing.T) {
	home := t.TempDir()
	httpsCalls := 0
	p := &Prober{
		SSH: func(context.Context) string { return "Connection reset by 20.200.245.247 port 22" },
		HTTPS: func(context.Context) Result {
			httpsCalls++
			return Result{Usable: true, Detail: "ok"}
		},
	}
	r, err := p.Run(context.Background(), home, true)
	if err != nil || r.After != ModeHTTPS || r.Changed {
		t.Fatalf("check run = %+v, %v", r, err)
	}
	if _, err := os.Stat(Path(home)); !os.IsNotExist(err) {
		t.Fatal("--check wrote the mode file")
	}
	if r, err = p.Run(context.Background(), home, false); err != nil || !r.Changed || r.Before != "" || r.After != ModeHTTPS {
		t.Fatalf("first run = %+v, %v", r, err)
	}
	p.SSH = func(context.Context) string { return "Hi x! You've successfully authenticated" }
	if r, err = p.Run(context.Background(), home, false); err != nil || !r.Changed || r.After != ModeSSH {
		t.Fatalf("recovery run = %+v, %v", r, err)
	}
	if httpsCalls != 2 {
		t.Fatalf("https probed %d times, want 2 (skipped while ssh is usable)", httpsCalls)
	}
}

func TestNeutralGitEnvDropsRepositorySelection(t *testing.T) {
	got := neutralGitEnv([]string{"HOME=/h", "GIT_DIR=/r/.git", "GIT_CONFIG_PARAMETERS='credential.helper=x'", "GIT_CONFIG_COUNT=1", "PATH=/bin"})
	if strings.Join(got, " ") != "HOME=/h PATH=/bin" {
		t.Fatalf("neutralGitEnv = %v", got)
	}
}
