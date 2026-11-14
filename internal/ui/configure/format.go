package configure

import (
	"fmt"
	"os"
	"strings"

	"github.com/STAIxBWLB/dotfiles/internal/sliceutil"
	"github.com/STAIxBWLB/dotfiles/internal/ui"
)

// printSection emits a section divider to os.Stdout. Ui-internal wrapper over
// ui.WriteSection used by the configure_*.go interactive flow, which always runs
// on a real TTY and does not go through cli.Printer.
func printSection(title string) { ui.WriteSection(os.Stdout, title) }

// printKV emits a KV row to os.Stdout. Ui-internal wrapper over ui.WriteKV used
// by PrintStateSummary (called from `dot init`/`reconfigure`/`preflight`).
func printKV(key, value string) { ui.WriteKV(os.Stdout, key, value) }

// formatBool returns a styled enabled/disabled indicator. The leading glyph
// comes from the marker alphabet (ui.MarkPresent / ui.MarkAbsent) so color and
// glyph stay semantically paired.
func formatBool(v bool) string {
	if v {
		return ui.StyleSuccess.Render(ui.MarkPresent) + " enabled"
	}
	return ui.StyleHint.Render(ui.MarkAbsent + " disabled")
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

// printLine routes native UI text through the same stream policy as reports.
func printLine(v ...any) { _, _ = fmt.Fprintln(ui.Output(os.Stdout), v...) }
