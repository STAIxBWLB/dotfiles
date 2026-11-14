package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/exec"
	"github.com/STAIxBWLB/dotfiles/internal/ui"
)

func pinColorEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{"NO_COLOR", "FORCE_COLOR", "CLICOLOR_FORCE", "CLICOLOR", "TERM"} {
		t.Setenv(key, "")
	}
}

func TestRootColorFlagAppliesToHumanReports(t *testing.T) {
	pinColorEnv(t)
	t.Setenv("HOME", t.TempDir())
	for _, tc := range []struct {
		name, flag string
		env        map[string]string
		want       bool
	}{
		{name: "auto pipe", flag: "auto"},
		{name: "force env pipe", flag: "auto", env: map[string]string{"FORCE_COLOR": "1"}, want: true},
		{name: "no color overrides force", flag: "auto", env: map[string]string{"NO_COLOR": "1", "FORCE_COLOR": "1"}},
		{name: "explicit always wins", flag: "always", env: map[string]string{"NO_COLOR": "1"}, want: true},
		{name: "explicit never wins", flag: "never", env: map[string]string{"FORCE_COLOR": "1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for key, value := range tc.env {
				t.Setenv(key, value)
			}
			out, errOut, err := runDotForTest("--color="+tc.flag, "usecase")
			if err != nil {
				t.Fatal(err)
			}
			if strings.ContainsRune(out, 27) != tc.want {
				t.Fatalf("color=%s, ESC=%v, want=%v", tc.flag, strings.ContainsRune(out, 27), tc.want)
			}
			if !strings.Contains(out, "dot Use Cases") || errOut != "" {
				t.Fatalf("report missing or stderr polluted: %q", errOut)
			}
		})
	}
}

func TestInvalidColorRejectedBeforeReportAndHelp(t *testing.T) {
	for _, args := range [][]string{{"--color=invalid", "usecase"}, {"--color=invalid", "--help"}} {
		out, _, err := runDotForTest(args...)
		if err == nil || !strings.Contains(err.Error(), "choose auto, always or never") || out != "" {
			t.Fatalf("args=%v: %q, %v", args, out, err)
		}
	}
}

func TestExistingJSONSchemaRemainsUncoloredWhenForced(t *testing.T) {
	pinColorEnv(t)
	t.Setenv("FORCE_COLOR", "1")
	home := t.TempDir()
	plain, _, err := runDotForTest("--home", home, "ai", "skills", "list", "--json", "--color=never")
	if err != nil {
		t.Fatal(err)
	}
	forced, errOut, err := runDotForTest("--home", home, "ai", "skills", "list", "--json", "--color=always")
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsRune(forced+errOut, 27) || !json.Valid([]byte(forced)) || forced != plain {
		t.Fatalf("JSON changed or contains ESC:\n%s\nstderr:%s", forced, errOut)
	}
}

func TestStatusQuietUnlessVerboseAndLoggerRestored(t *testing.T) {
	pinColorEnv(t)
	_, home := newStatusHomeFixture(t)
	previous := slog.Default()
	for _, verbose := range []bool{false, true} {
		args := []string{"--home", home, "status", "--color=never"}
		if verbose {
			args = append(args, "--verbose")
		}
		out, errOut, err := runDotForTest(args...)
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		if !strings.Contains(out, "dot Status") || strings.Contains(out, "level=") {
			t.Fatalf("status payload/log mix: %s", out)
		}
		if strings.Contains(errOut, "level=DEBUG") != verbose {
			t.Fatalf("verbose=%v, stderr=%q", verbose, errOut)
		}
		if slog.Default() != previous {
			t.Fatal("default logger escaped invocation")
		}
	}
}

func TestCommandPresentationRestoresOnFailureAndRoutesLibraryLogs(t *testing.T) {
	pinColorEnv(t)
	previous := slog.Default()
	for _, verbose := range []bool{false, true} {
		root := NewRootCmd("dev", "test")
		probe := &cobra.Command{Use: "probe", RunE: func(cmd *cobra.Command, _ []string) error {
			// A library's default logger and the explicit runner path must follow
			// the same policy, while a user-facing warning remains visible.
			slog.Info("library info")
			slog.Warn("library warning")
			runner := exec.NewRunner(true, commandLogger(cmd))
			if _, err := runner.Run(cmd.Context(), "fixture-command"); err != nil {
				return err
			}
			printerFrom(cmd).Warn("user warning")
			return errors.New("fixture failure")
		}}
		bindPresentation(probe)
		root.AddCommand(probe)
		var out, errOut bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&errOut)
		args := []string{"probe", "--color=never"}
		if verbose {
			args = append(args, "--verbose")
		}
		root.SetArgs(args)
		if err := root.Execute(); err == nil {
			t.Fatal("missing fixture failure")
		}
		for _, message := range []string{"library info", "library warning", "fixture-command"} {
			if strings.Contains(errOut.String(), message) != verbose {
				t.Fatalf("verbose=%v, missing/wrong log %s: %s", verbose, message, errOut.String())
			}
		}
		if !strings.Contains(errOut.String(), "user warning") || out.Len() != 0 {
			t.Fatal("user diagnostic routing changed")
		}
		if slog.Default() != previous {
			t.Fatal("logger leaked after error")
		}
		var after bytes.Buffer
		ui.WriteHeader(&after, "outside")
		if strings.ContainsRune(after.String(), 27) {
			t.Fatal("color policy leaked after error")
		}
	}
}

func TestPrinterKeepsPerStreamPolicyAndNativeReportPaths(t *testing.T) {
	pinColorEnv(t)
	var out, errOut bytes.Buffer
	// The sinks are bound independently. Printer must not ask lipgloss to
	// auto-detect the pipe again and erase the enabled stdout styling.
	p := &Printer{Out: ui.NewColorWriter(&out, ui.ColorAlways, false), Err: ui.NewColorWriter(&errOut, ui.ColorNever, false)}
	p.Success("ok")
	p.Warn("warning")
	if !strings.ContainsRune(out.String(), 27) || strings.ContainsRune(errOut.String(), 27) {
		t.Fatalf("stream policy: out=%q err=%q", out.String(), errOut.String())
	}
	// Native UI output is scoped too; no environment variable is changed.
	old := os.Getenv("NO_COLOR")
	restore := ui.SetColorPolicy(ui.ColorNever, false)
	_, err := io.WriteString(ui.Output(&out), ui.StyleSuccess.Render("native"))
	restore()
	if err != nil || os.Getenv("NO_COLOR") != old {
		t.Fatal("native output changed the environment")
	}
}

// Presentation flags control dot's own UI, never the payload of a wrapped
// child command, which can include escape sequences or arbitrary bytes.
func TestPresentationPreservesRawChildPayload(t *testing.T) {
	pinColorEnv(t)
	cmd := &cobra.Command{Use: "payload"}
	mode := ui.ColorNever
	cmd.Flags().Var(&mode, "color", "")
	var out bytes.Buffer
	cmd.SetOut(&out)
	raw := []byte{0, 27, '[', '3', '1', 'm', 'x', 27, '[', '0', 'm'}
	err := withPresentation(cmd, func() error { _, err := cmd.OutOrStdout().Write(raw); return err })
	if err != nil || !bytes.Equal(raw, out.Bytes()) {
		t.Fatalf("raw payload changed: %q, %v", out.Bytes(), err)
	}
}

func TestPresentationRestoresStandardLogBridge(t *testing.T) {
	writer, flags, prefix := log.Writer(), log.Flags(), log.Prefix()
	defer func() { log.SetOutput(writer); log.SetFlags(flags); log.SetPrefix(prefix) }()
	var captured bytes.Buffer
	log.SetOutput(&captured)
	log.SetFlags(log.Lshortfile)
	log.SetPrefix("before: ")
	cmd := &cobra.Command{Use: "probe"}
	cmd.Flags().Bool("verbose", true, "")
	cmd.SetErr(io.Discard)
	_ = withPresentation(cmd, func() error { return errors.New("fixture") })
	if log.Writer() != &captured || log.Flags() != log.Lshortfile || log.Prefix() != "before: " {
		t.Fatal("standard logger bridge leaked across invocation")
	}
	log.Print("after")
	if !strings.Contains(captured.String(), "after") {
		t.Fatalf("standard logger sink lost: %q", captured.String())
	}
}
