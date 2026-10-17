package ui

import (
	"io"
	"os"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/STAIxBWLB/dotfiles/internal/sliceutil"
)

const kvKeyWidth = 22 // width of "Pull+intake scheduler:"

// WriteHeader writes a styled report title to w with one leading blank line.
// The title is rendered with a single space of horizontal padding so the
// blue StyleHeader background frames the text cleanly.
func WriteHeader(w io.Writer, title string) {
	_, _ = lipgloss.Fprintln(w)
	_, _ = lipgloss.Fprintln(w, StyleHeader.Render(" "+title+" "))
}

// WriteSection writes a section divider with one leading blank line and the
// canonical "▸ " prefix. Callers pass the section name only.
func WriteSection(w io.Writer, title string) {
	_, _ = lipgloss.Fprintln(w)
	_, _ = lipgloss.Fprintln(w, StyleSection.Render("▸ "+title))
}

// WriteKV writes a styled key/value row at the 2-space top-level indent.
// An empty value renders as "(unset)" in the hint style.
func WriteKV(w io.Writer, key, value string) {
	if value == "" {
		value = StyleHint.Render("(unset)")
	} else {
		value = StyleValue.Render(value)
	}
	label := key + ":"
	width := kvKeyWidth
	if labelWidth := lipgloss.Width(label); labelWidth > width {
		width = labelWidth
	}
	_, _ = lipgloss.Fprintf(w, "  %s  %s\n", StyleKey.Width(width).Render(label), value)
}

// printSection emits a section divider to os.Stdout. Ui-internal wrapper over
// WriteSection used by the configure_*.go interactive flow, which always runs
// on a real TTY and does not go through cli.Printer.
func printSection(title string) { WriteSection(os.Stdout, title) }

// printKV emits a KV row to os.Stdout. Ui-internal wrapper over WriteKV used
// by PrintStateSummary (called from `dot init`/`reconfigure`/`preflight`).
func printKV(key, value string) { WriteKV(os.Stdout, key, value) }

// formatBool returns a styled enabled/disabled indicator. The leading glyph
// comes from the marker alphabet (MarkPresent / MarkAbsent) so color and
// glyph stay semantically paired.
func formatBool(v bool) string {
	if v {
		return StyleSuccess.Render(MarkPresent) + " enabled"
	}
	return StyleHint.Render(MarkAbsent + " disabled")
}

// splitCaskList parses a whitespace-separated cask list into a clean,
// de-duplicated slice.
func splitCaskList(s string) []string {
	return sliceutil.Dedupe(strings.Fields(s))
}

func pickBackupChoice(path string, detectedDrive bool) string {
	if detectedDrive {
		return "cloud (auto-detected)"
	}
	if strings.Contains(path, "/.local/share/dotfiles/") {
		return "local"
	}
	if path != "" {
		return "custom"
	}
	return "local"
}
