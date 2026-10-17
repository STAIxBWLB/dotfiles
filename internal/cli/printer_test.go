package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/ui"
	"github.com/spf13/cobra"
)

func TestPrinter_LineAndWarnRouteToDistinctSinks(t *testing.T) {
	var out, errb bytes.Buffer
	p := &Printer{Out: &out, Err: &errb}

	p.Line("hello %s", "world")
	p.Warn("oops %d", 42)
	p.Raw("tail")

	if got := out.String(); got != "hello world\ntail" {
		t.Errorf("Out = %q, want %q", got, "hello world\ntail")
	}
	// Warn uses StyleWarning.Render; in a non-TTY buffer lipgloss emits
	// the raw string without ANSI escapes.
	if got := errb.String(); got != "oops 42\n" {
		t.Errorf("Err = %q, want %q", got, "oops 42\n")
	}
}

func TestPrinterFrom_CapturesCobraOutputStreams(t *testing.T) {
	var out, errb bytes.Buffer
	cmd := &cobra.Command{Use: "dummy"}
	cmd.SetOut(&out)
	cmd.SetErr(&errb)

	p := printerFrom(cmd)
	p.Line("captured stdout")
	p.Warn("captured stderr")

	if !strings.Contains(out.String(), "captured stdout") {
		t.Errorf("stdout capture missing: %q", out.String())
	}
	if !strings.Contains(errb.String(), "captured stderr") {
		t.Errorf("stderr capture missing: %q", errb.String())
	}
}

func TestPrinterFrom_NilCmdFallsBackToOSStreams(t *testing.T) {
	p := printerFrom(nil)
	if p.Out == nil || p.Err == nil {
		t.Fatalf("nil sinks: Out=%v Err=%v", p.Out, p.Err)
	}
}

// TestPrinter_HeaderEmitsLeadingBlank locks in the blank-line contract:
// Header precedes its title with exactly one blank line and none after.
func TestPrinter_HeaderEmitsLeadingBlank(t *testing.T) {
	var out bytes.Buffer
	(&Printer{Out: &out}).Header("Report Title")

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines (blank + title), got %d: %q", len(lines), out.String())
	}
	if lines[0] != "" {
		t.Errorf("first line should be blank, got %q", lines[0])
	}
	if !strings.Contains(lines[1], "Report Title") {
		t.Errorf("title line missing text: %q", lines[1])
	}
}

// TestPrinter_SectionEmitsLeadingBlankAndPrefix locks in the "▸ " prefix
// and the blank-before-none-after contract.
func TestPrinter_SectionEmitsLeadingBlankAndPrefix(t *testing.T) {
	var out bytes.Buffer
	(&Printer{Out: &out}).Section("System")

	got := out.String()
	if !strings.HasPrefix(got, "\n") {
		t.Errorf("section output should begin with a blank line, got %q", got)
	}
	if !strings.Contains(got, "▸ System") {
		t.Errorf("section output missing '▸ System' prefix: %q", got)
	}
	if strings.Count(got, "\n") != 2 {
		t.Errorf("section should emit exactly one blank + one title line (2 newlines), got %d in %q",
			strings.Count(got, "\n"), got)
	}
}

// TestPrinter_BulletIndentsTwoSpaces locks in the canonical row layout.
func TestPrinter_BulletIndentsTwoSpaces(t *testing.T) {
	var out bytes.Buffer
	(&Printer{Out: &out}).Bullet(ui.MarkPresent, "ready")

	want := "  ✓  ready\n"
	if got := out.String(); got != want {
		t.Errorf("bullet layout = %q, want %q", got, want)
	}
}

// TestPrinter_KVRendersLabelAndValue exercises the existing KV path after
// the delegation refactor.
func TestPrinter_KVRendersLabelAndValue(t *testing.T) {
	var out bytes.Buffer
	(&Printer{Out: &out}).KV("Profile", "full")

	got := out.String()
	if !strings.Contains(got, "Profile:") {
		t.Errorf("KV missing label: %q", got)
	}
	if !strings.Contains(got, "full") {
		t.Errorf("KV missing value: %q", got)
	}
	if !strings.HasPrefix(got, "  ") {
		t.Errorf("KV should indent 2 spaces, got %q", got)
	}
}

// TestPrinter_KVEmptyValueRendersUnset checks the placeholder contract.
func TestPrinter_KVEmptyValueRendersUnset(t *testing.T) {
	var out bytes.Buffer
	(&Printer{Out: &out}).KV("Profile", "")
	if !strings.Contains(out.String(), "(unset)") {
		t.Errorf("empty value should render as (unset), got %q", out.String())
	}
}

func TestPrinter_KVLongLabelDoesNotWrap(t *testing.T) {
	var short, long bytes.Buffer
	(&Printer{Out: &short}).KV("Profile", "full")
	(&Printer{Out: &long}).KV("Pull+intake scheduler", "stopped")

	shortRow := short.String()
	longRow := long.String()
	if strings.Contains(longRow, "Pull+intake\nscheduler") {
		t.Errorf("long KV label wrapped: %q", longRow)
	}
	if !strings.Contains(longRow, "Pull+intake scheduler:") || !strings.Contains(longRow, "stopped") {
		t.Errorf("long KV row missing label or value: %q", longRow)
	}
	if shortCol, longCol := strings.Index(shortRow, "full"), strings.Index(longRow, "stopped"); shortCol != longCol {
		t.Errorf("KV values should align: short=%q long=%q", shortRow, longRow)
	}
}

// TestPrinter_BlankEmitsSingleNewline is a small guard that Blank is a
// one-byte emitter — callers should be able to use it interchangeably with
// `p.Line("")`.
func TestPrinter_BlankEmitsSingleNewline(t *testing.T) {
	var out bytes.Buffer
	(&Printer{Out: &out}).Blank()
	if got := out.String(); got != "\n" {
		t.Errorf("Blank() = %q, want single newline", got)
	}
}

// TestPrinter_SuccessAndFailRouteToCorrectSinks verifies the stream policy:
// Success → Out, Fail → Err.
func TestPrinter_SuccessAndFailRouteToCorrectSinks(t *testing.T) {
	var out, errb bytes.Buffer
	p := &Printer{Out: &out, Err: &errb}

	p.Success("installed %d cask(s)", 3)
	p.Fail("catastrophe: %s", "oops")

	if !strings.Contains(out.String(), "installed 3 cask(s)") {
		t.Errorf("Success should write to Out, got stdout=%q", out.String())
	}
	if !strings.Contains(errb.String(), "catastrophe: oops") {
		t.Errorf("Fail should write to Err, got stderr=%q", errb.String())
	}
	if strings.Contains(out.String(), "catastrophe") {
		t.Error("Fail leaked to stdout")
	}
	if strings.Contains(errb.String(), "installed") {
		t.Error("Success leaked to stderr")
	}
}
