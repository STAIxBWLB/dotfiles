package ui

import (
	"fmt"

	"charm.land/lipgloss/v2"
)

// Role is the semantic API for CLI styles. Palette entries live only here.
type Role string

const (
	RoleHeader  Role = "header"
	RoleSection Role = "section"
	RoleKey     Role = "key"
	RoleValue   Role = "value"
	RoleSuccess Role = "success"
	RoleWarning Role = "warning"
	RoleError   Role = "error"
	RoleHint    Role = "hint"
	RoleAccent  Role = "accent"
)

var roles = []Role{RoleHeader, RoleSection, RoleKey, RoleValue, RoleSuccess, RoleWarning, RoleError, RoleHint, RoleAccent}

type palette struct {
	background string // reference terminal background; never painted by dot
	foreground string // reference default foreground; values never override it
	colors     map[Role]string
}

var palettes = map[string]palette{
	"ansi":              {colors: map[Role]string{RoleHeader: "4", RoleSection: "5", RoleKey: "4", RoleValue: "", RoleSuccess: "2", RoleWarning: "3", RoleError: "1", RoleHint: "8", RoleAccent: "6"}},
	"tokyo-night-dark":  {background: "#1A1B26", foreground: "#C0CAF5", colors: map[Role]string{RoleHeader: "#7AA2F7", RoleSection: "#BB9AF7", RoleKey: "#7AA2F7", RoleValue: "", RoleSuccess: "#9ECE6A", RoleWarning: "#E0AF68", RoleError: "#F7768E", RoleHint: "#787FAF", RoleAccent: "#7DCFFF"}},
	"tokyo-night-light": {background: "#E1E2E7", foreground: "#343B58", colors: map[Role]string{RoleHeader: "#245DB0", RoleSection: "#7847BD", RoleKey: "#245DB0", RoleValue: "", RoleSuccess: "#326322", RoleWarning: "#765019", RoleError: "#A5263F", RoleHint: "#747C9B", RoleAccent: "#006C86"}},
}

func roleStyle(p palette, role Role) lipgloss.Style {
	style := lipgloss.NewStyle()
	if color := p.colors[role]; color != "" {
		style = style.Foreground(lipgloss.Color(color))
	}
	switch role {
	case RoleHeader, RoleSection, RoleSuccess, RoleWarning, RoleError:
		style = style.Bold(true)
	}
	return style
}

// Named styles preserve existing report call sites. Their values are scoped to
// one CLI invocation by SetPresentation; callers use Render rather than assign.
var (
	StyleHeader  = roleStyle(palettes["ansi"], RoleHeader).Padding(0, 1)
	StyleSection = roleStyle(palettes["ansi"], RoleSection)
	StyleKey     = roleStyle(palettes["ansi"], RoleKey).Width(14)
	StyleValue   = roleStyle(palettes["ansi"], RoleValue)
	StyleSuccess = roleStyle(palettes["ansi"], RoleSuccess)
	StyleWarning = roleStyle(palettes["ansi"], RoleWarning)
	StyleError   = roleStyle(palettes["ansi"], RoleError)
	StyleHint    = roleStyle(palettes["ansi"], RoleHint)
	StyleAccent  = roleStyle(palettes["ansi"], RoleAccent)
)

type styleSet struct{ header, section, key, value, success, warning, failure, hint, accent lipgloss.Style }

func captureStyles() styleSet {
	return styleSet{StyleHeader, StyleSection, StyleKey, StyleValue, StyleSuccess, StyleWarning, StyleError, StyleHint, StyleAccent}
}
func (s styleSet) apply() {
	StyleHeader, StyleSection, StyleKey, StyleValue, StyleSuccess, StyleWarning, StyleError, StyleHint, StyleAccent = s.header, s.section, s.key, s.value, s.success, s.warning, s.failure, s.hint, s.accent
}
func stylesFor(p palette) styleSet {
	return styleSet{roleStyle(p, RoleHeader).Padding(0, 1), roleStyle(p, RoleSection), roleStyle(p, RoleKey).Width(14), roleStyle(p, RoleValue), roleStyle(p, RoleSuccess), roleStyle(p, RoleWarning), roleStyle(p, RoleError), roleStyle(p, RoleHint), roleStyle(p, RoleAccent)}
}

// SetPresentation selects an explicitly requested theme and locale markers for
// one command. The CLI executes one command per process; the caller defers the
// restoration even on error. No environment variable or user setting is changed.
func SetPresentation(getenv func(string) string, ascii bool) (func(), error) {
	name := getenv("DOT_COLOR_THEME")
	if name == "" {
		name = "ansi"
	}
	p, ok := palettes[name]
	if !ok {
		return nil, fmt.Errorf("invalid DOT_COLOR_THEME %q: choose ansi, tokyo-night-dark or tokyo-night-light", name)
	}
	previousStyles, previousMarkers := captureStyles(), captureMarkers()
	stylesFor(p).apply()
	markersFor(getenv, ascii).apply()
	return func() { previousStyles.apply(); previousMarkers.apply() }, nil
}

// RoleStyle returns the current semantic style. Named aliases remain available
// for existing reports; new renderers can use the role vocabulary directly.
func RoleStyle(role Role) lipgloss.Style {
	switch role {
	case RoleHeader:
		return StyleHeader
	case RoleSection:
		return StyleSection
	case RoleKey:
		return StyleKey
	case RoleSuccess:
		return StyleSuccess
	case RoleWarning:
		return StyleWarning
	case RoleError:
		return StyleError
	case RoleHint:
		return StyleHint
	case RoleAccent:
		return StyleAccent
	default:
		return StyleValue
	}
}
