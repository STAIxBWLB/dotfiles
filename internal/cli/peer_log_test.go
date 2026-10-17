package cli

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every scheduled peer run leaves a dated start line and a dated exit line
// with its code (#270 AC1).
func TestScheduledPeerLogWritesStartAndExitLines(t *testing.T) {
	prev := redirectStdio
	redirectStdio = func(*os.File) error { return nil }
	t.Cleanup(func() { redirectStdio = prev })
	path := filepath.Join(t.TempDir(), "log", "sync.log")

	for _, runErr := range []error{nil, errors.New("peer plan safety: refusing 3 outbound deletion(s)\nmore")} {
		finish, err := startScheduledPeerLog(path)
		if err != nil {
			t.Fatal(err)
		}
		finish(runErr)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	stamp := `^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2} op=peer-sync `
	want := []string{
		stamp + `start pid=\d+$`,
		stamp + `exit=0$`,
		stamp + `start pid=\d+$`,
		stamp + `exit=1 err=peer plan safety: refusing 3 outbound deletion\(s\)$`,
	}
	if len(lines) != len(want) {
		t.Fatalf("log = %q, want %d lines", lines, len(want))
	}
	for i, re := range want {
		if !regexp.MustCompile(re).MatchString(lines[i]) {
			t.Errorf("line %d = %q, want %s", i, lines[i], re)
		}
	}
}

// The log rotates once it passes the limit and keeps the configured number of
// old files (#270 AC2).
func TestRotateLogFileKeepsConfiguredCount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sync.log")
	for i := 0; i < 5; i++ {
		if err := os.WriteFile(path, []byte(strings.Repeat("x", 20)+string(rune('a'+i))), 0o644); err != nil {
			t.Fatal(err)
		}
		rotateLogFile(path, 10, 3)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("the oversized log was not rotated away")
	}
	for i, want := range map[int]string{1: "e", 2: "d", 3: "c"} {
		data, err := os.ReadFile(path + "." + string(rune('0'+i)))
		if err != nil || !strings.HasSuffix(string(data), want) {
			t.Errorf("%s.%d = %q, %v; want the run ending %q", path, i, data, err, want)
		}
	}
	if _, err := os.Stat(path + ".4"); !os.IsNotExist(err) {
		t.Error("kept more than 3 old files")
	}
	// Under the limit nothing moves.
	if err := os.WriteFile(path, []byte("small"), 0o644); err != nil {
		t.Fatal(err)
	}
	rotateLogFile(path, 10, 3)
	if _, err := os.Stat(path); err != nil {
		t.Fatal("a log under the limit was rotated")
	}
}

// A run that fails before its log opens still leaves a dated exit line.
func TestPeerExitLineIsDated(t *testing.T) {
	line := peerExitLine(errors.New("loading peer config: bad\nmore"))
	if !regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2} op=peer-sync exit=1 err=loading peer config: bad$`).MatchString(line) {
		t.Fatalf("line = %q", line)
	}
}
