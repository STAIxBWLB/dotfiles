package cli

import (
	"fmt"
	"io"
	"os"

	"charm.land/lipgloss/v2"
	"github.com/spf13/cobra"

	"github.com/STAIxBWLB/dotfiles/internal/ui"
)

// Printer routes CLI output to writable sinks so tests can capture output.
// Obtain one via printerFrom(cmd); commands that don't have a cobra context
// can construct one manually with Printer{Out: ..., Err: ...}.
//
// Layout contract (see internal/ui/markers.go for the glyph alphabet):
//
//  1. Blank-line policy. Header() and Section() each emit exactly one blank
//     line before the title and none after. Callers should not hand-roll
//     `p.Line("")` around them; use Blank() when an explicit separator is
//     needed mid-section.
//  2. Indent policy. KV() and Bullet() lead with two spaces — the canonical
//     top-level indent. Nested detail uses four spaces written by the caller.
//  3. Stream policy. Line / Header / Section / Bullet / KV / Success / Raw
//     go to Out. Warn (yellow) and Fail (red) go to Err.
type Printer struct {
	Out io.Writer
	Err io.Writer
}

// printerFrom returns a Printer wired to the command's output streams.
// Falls back to os.Stdout/os.Stderr when cmd is nil (defensive — cobra
// always populates these in practice).
func printerFrom(cmd *cobra.Command) *Printer {
	if cmd == nil {
		return &Printer{Out: os.Stdout, Err: os.Stderr}
	}
	return &Printer{Out: cmd.OutOrStdout(), Err: cmd.ErrOrStderr()}
}

// Line prints a formatted line to Out with a trailing newline.
func (p *Printer) Line(format string, a ...any) {
	_, _ = lipgloss.Fprintf(p.Out, format+"\n", a...)
}

// Blank emits a single blank line. Prefer this over p.Line("") at call sites.
func (p *Printer) Blank() {
	fmt.Fprintln(p.Out)
}

// Raw prints to Out without adding a newline (for progress dots, prompts).
func (p *Printer) Raw(format string, a ...any) {
	_, _ = lipgloss.Fprintf(p.Out, format, a...)
}

// Header renders a top-level report title. One blank line precedes the
// rendered title; none follows. Typical usage at the top of a report
// handler: `p.Header("dot Status")` — the surrounding padding is
// applied by ui.StyleHeader.
func (p *Printer) Header(title string) { ui.WriteHeader(p.Out, title) }

// Section renders a section divider inside a report. One blank line
// precedes the rendered title; none follows. The "▸ " prefix is added
// automatically so callers pass just the section name.
func (p *Printer) Section(title string) { ui.WriteSection(p.Out, title) }

// Bullet renders a list row at the canonical 2-space indent: "  <marker>  <text>".
// Marker comes from ui.Mark* constants; caller-provided style (if any) is
// applied to marker via lipgloss Render before calling.
func (p *Printer) Bullet(marker, text string) {
	_, _ = lipgloss.Fprintf(p.Out, "  %s  %s\n", marker, text)
}

// KV renders a styled key/value line. Empty values render as "(unset)" in
// the hint style so the column alignment stays consistent across calls.
func (p *Printer) KV(key, value string) { ui.WriteKV(p.Out, key, value) }

// Success prints a formatted line to Out styled with StyleSuccess.
// Use for "✓ done" style confirmations.
func (p *Printer) Success(format string, a ...any) {
	_, _ = lipgloss.Fprintln(p.Out, ui.StyleSuccess.Render(fmt.Sprintf(format, a...)))
}

// Warn prints a formatted line to Err styled with StyleWarning (orange).
// Use for recoverable / attention-needed conditions.
func (p *Printer) Warn(format string, a ...any) {
	_, _ = lipgloss.Fprintln(p.Err, ui.StyleWarning.Render(fmt.Sprintf(format, a...)))
}

// Fail prints a formatted line to Err styled with StyleError (red).
// Use for hard failures that precede a non-zero exit. Named Fail rather
// than Err to avoid collision with the Err field.
func (p *Printer) Fail(format string, a ...any) {
	_, _ = lipgloss.Fprintln(p.Err, ui.StyleError.Render(fmt.Sprintf(format, a...)))
}
