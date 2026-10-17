package ghauth

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestAuthenticated(t *testing.T) {
	tests := []struct {
		name     string
		script   string
		noBinary bool
		dryRun   bool
		want     bool
	}{
		{
			name:   "authenticated",
			script: "#!/bin/sh\nexit 0\n",
			want:   true,
		},
		{
			name:   "not authenticated",
			script: "#!/bin/sh\nexit 1\n",
			want:   false,
		},
		{
			name:     "gh missing",
			noBinary: true,
			want:     false,
		},
		{
			// RunQuery always executes, so detection works in dry-run mode too.
			name:   "works in dry-run mode",
			script: "#!/bin/sh\nexit 0\n",
			dryRun: true,
			want:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bin := t.TempDir()
			if !tt.noBinary {
				writeExecutable(t, filepath.Join(bin, "gh"), tt.script)
			}
			t.Setenv("PATH", bin)
			runner := exec.NewRunner(tt.dryRun, quietLogger())

			if got := Authenticated(runner); got != tt.want {
				t.Errorf("Authenticated = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLogin_ArgConstruction(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args.txt")
	bin := t.TempDir()
	writeExecutable(t, filepath.Join(bin, "gh"),
		"#!/bin/sh\necho \"$@\" > "+argsFile+"\n")
	t.Setenv("PATH", bin)
	runner := exec.NewRunner(false, quietLogger())

	if err := Login(context.Background(), runner); err != nil {
		t.Fatalf("Login: %v", err)
	}

	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("reading args file: %v", err)
	}
	line := strings.TrimSpace(string(data))

	if !strings.HasPrefix(line, "auth login --hostname github.com --git-protocol https --web") {
		t.Errorf("unexpected base args: %s", line)
	}
	if got, want := strings.Count(line, "--scopes "), len(Scopes); got != want {
		t.Errorf("got %d --scopes pairs, want %d: %s", got, want, line)
	}
	for _, s := range Scopes {
		if !strings.Contains(line, "--scopes "+s) {
			t.Errorf("missing scope %q in args: %s", s, line)
		}
	}
}

func TestLogin_DryRunDoesNotInvokeGh(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args.txt")
	bin := t.TempDir()
	writeExecutable(t, filepath.Join(bin, "gh"),
		"#!/bin/sh\necho \"$@\" > "+argsFile+"\n")
	t.Setenv("PATH", bin)
	runner := exec.NewRunner(true, quietLogger())

	if err := Login(context.Background(), runner); err != nil {
		t.Fatalf("Login dry-run: %v", err)
	}
	if _, err := os.Stat(argsFile); !os.IsNotExist(err) {
		t.Error("dry-run must not invoke gh")
	}
}
