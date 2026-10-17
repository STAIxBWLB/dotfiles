package ui

import (
	"errors"
	"fmt"
	"os"
	"regexp"

	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/term"

	"github.com/STAIxBWLB/dotfiles/internal/sliceutil"
)

// Styles for consistent output across the CLI.
//
// Semantic contract for the color layer (glyphs live in markers.go):
//   - StyleHeader   page/report titles — blue, bold, padded
//   - StyleSection  section dividers   — purple, bold
//   - StyleKey      left-side labels in key/value rows
//   - StyleValue    values in key/value rows, emphasized list items
//   - StyleSuccess  positive status (MarkPresent, MarkStarred) — green, bold
//   - StyleWarning  non-fatal attention (MarkWarn) — orange, bold
//   - StyleError    hard failure / non-zero exit paths (MarkFail) — red, bold
//   - StyleHint     secondary info, neutral missing, (unset) placeholders
var (
	StyleHeader = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#7AA2F7")).
			BorderForeground(lipgloss.Color("#7AA2F7")).
			Padding(0, 1)

	// StyleSection intentionally has no MarginTop — the leading blank line is
	// emitted by cli.Printer.Section / ui.WriteSection so the contract stays
	// explicit and assertable in tests.
	StyleSection = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#BB9AF7"))

	StyleKey = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#9ECE6A")).
			Width(14)

	StyleValue = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#C0CAF5"))

	StyleSuccess = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#9ECE6A")).
			Bold(true)

	StyleWarning = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#E0AF68")).
			Bold(true)

	StyleError = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#F7768E")).
			Bold(true)

	StyleHint = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#565F89"))
)

// TerminalAttached reports whether the prompts below can run: huh's
// accessible mode (TERM=dumb) needs no terminal; otherwise bubbletea's rule
// applies, stdin when it is a terminal, else /dev/tty.
// Without either (a plain ssh command, launchd, CI) huh dies with
// "bubbletea: could not open TTY" (#183). A variable so tests can pin it.
var TerminalAttached = func() bool {
	// TERM=dumb puts huh in accessible mode, which reads stdin and writes
	// stdout directly, with no terminal at all.
	if os.Getenv("TERM") == "dumb" {
		return true
	}
	// isatty, not a character-device check: /dev/null is a character device.
	if term.IsTerminal(os.Stdin.Fd()) {
		return true
	}
	f, err := os.Open("/dev/tty")
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

// ErrNoTerminal is what a prompt returns when it has no terminal to run on.
var ErrNoTerminal = errors.New("no terminal for an interactive prompt; pass --yes, or run from a terminal")

// ansiRe matches the SGR sequences lipgloss adds to prompt titles.
var ansiRe = regexp.MustCompile("\x1b\\[[0-9;]*m")

func noTerminal(message string) error {
	if TerminalAttached() {
		return nil
	}
	return fmt.Errorf("%s: %w", ansiRe.ReplaceAllString(message, ""), ErrNoTerminal)
}

// Confirm asks for yes/no. Returns true immediately if unattended.
func Confirm(message string, unattended bool) (bool, error) {
	if unattended {
		return true, nil
	}
	if err := noTerminal(message); err != nil {
		return false, err
	}
	var confirmed bool
	err := huh.NewConfirm().
		Title(message).
		Value(&confirmed).
		Run()
	return confirmed, err
}

// Select presents a choice. Returns defaultVal if unattended.
// Default is pre-selected in the list.
func Select(message string, options []string, defaultVal string, unattended bool) (string, error) {
	if unattended {
		return defaultVal, nil
	}
	if err := noTerminal(message); err != nil {
		return defaultVal, err
	}
	selected := defaultVal
	opts := make([]huh.Option[string], len(options))
	for i, o := range options {
		opts[i] = huh.NewOption(o, o)
	}
	err := huh.NewSelect[string]().
		Title(message).
		Options(opts...).
		Value(&selected).
		Run()
	if err != nil {
		return defaultVal, err
	}
	return selected, nil
}

// Input asks for text input. Pre-filled with defaultVal so user can edit
// existing value directly instead of retyping. Returns defaultVal if unattended.
func Input(message, defaultVal string, unattended bool) (string, error) {
	if unattended {
		return defaultVal, nil
	}
	if err := noTerminal(message); err != nil {
		return defaultVal, err
	}
	// Seed value with default so user sees and can edit it
	value := defaultVal
	input := huh.NewInput().
		Title(message).
		Value(&value)
	if defaultVal == "" {
		input = input.Placeholder("(empty)")
	}
	if err := input.Run(); err != nil {
		return defaultVal, err
	}
	if value == "" {
		return defaultVal, nil
	}
	return value, nil
}

// InputWithDetected is like Input but marks a system-detected default.
// Displays "(auto-detected)" hint when defaultVal comes from system detection.
func InputWithDetected(message, defaultVal string, detected bool, unattended bool) (string, error) {
	if detected && defaultVal != "" {
		message = message + " " + StyleHint.Render("(auto-detected)")
	}
	return Input(message, defaultVal, unattended)
}

// MultiSelect presents a checkbox-style multi-select. Returns defaultVals
// unchanged if unattended. Options pre-selected where they appear in defaultVals.
func MultiSelect(message string, options, defaultVals []string, unattended bool) ([]string, error) {
	if unattended {
		return defaultVals, nil
	}
	if err := noTerminal(message); err != nil {
		return defaultVals, err
	}
	selected := append([]string(nil), defaultVals...)
	opts := make([]huh.Option[string], len(options))
	for i, o := range options {
		opts[i] = huh.NewOption(o, o).Selected(sliceutil.Contains(defaultVals, o))
	}
	err := huh.NewMultiSelect[string]().
		Title(message).
		Options(opts...).
		Value(&selected).
		Run()
	if err != nil {
		return defaultVals, err
	}
	return selected, nil
}

// SelectOption lets callers show a human label while persisting a compact value.
type SelectOption struct {
	Label string
	Value string
}

// MultiSelectLabeled is MultiSelect for options whose displayed labels differ
// from the stored values.
func MultiSelectLabeled(message string, options []SelectOption, defaultVals []string, unattended bool) ([]string, error) {
	if unattended {
		return defaultVals, nil
	}
	if err := noTerminal(message); err != nil {
		return defaultVals, err
	}
	selected := append([]string(nil), defaultVals...)
	opts := make([]huh.Option[string], len(options))
	for i, o := range options {
		opts[i] = huh.NewOption(o.Label, o.Value).Selected(sliceutil.Contains(defaultVals, o.Value))
	}
	err := huh.NewMultiSelect[string]().
		Title(message).
		Options(opts...).
		Value(&selected).
		Run()
	if err != nil {
		return defaultVals, err
	}
	return selected, nil
}

// ConfirmBool asks for a bool. Returns defaultVal if unattended.
func ConfirmBool(message string, defaultVal, unattended bool) (bool, error) {
	if unattended {
		return defaultVal, nil
	}
	if err := noTerminal(message); err != nil {
		return defaultVal, err
	}
	value := defaultVal
	err := huh.NewConfirm().
		Title(message).
		Value(&value).
		Run()
	return value, err
}
