package cli

import (
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/ui"
)

// markerAlphabet is every glyph internal/ui publishes. The membership row
// below uses it to catch an inline glyph literal, which markers.go's own
// header comment forbids.
var markerAlphabet = map[string]string{
	ui.MarkPresent:   "MarkPresent",
	ui.MarkAbsent:    "MarkAbsent / MarkFail", // one glyph, the style disambiguates
	ui.MarkPartial:   "MarkPartial",
	ui.MarkStarred:   "MarkStarred",
	ui.MarkPreferred: "MarkPreferred",
	ui.MarkPending:   "MarkPending",
	ui.MarkWarn:      "MarkWarn",
}

// TestAppsStatusMarkerRendersThreeDistinctStates pins BUG-10: appsettings.
// StatusApp carries both Installed and InstallKnown, so `dot apps status` can
// tell "Homebrew says this cask is absent" from "Homebrew could not be asked",
// and the display must not collapse the two back together.
//
// The assertion is pairwise distinctness, not merely that the unknown state
// has some value: before the fix the unknown arm and the not-installed arm
// both rendered MarkPartial, which any single-state assertion would accept.
func TestAppsStatusMarkerRendersThreeDistinctStates(t *testing.T) {
	rows := []struct {
		name         string
		installKnown bool
		installed    bool
		wantMark     string
	}{
		{"installed", true, true, ui.MarkPresent},
		{"known and not installed", true, false, ui.MarkAbsent},
		{"install state unknown", false, false, ui.MarkPartial},
	}

	seen := make(map[string]string, len(rows))
	for _, row := range rows {
		mark, style := appsInstallMarker(row.installKnown, row.installed)
		if mark != row.wantMark {
			t.Errorf("%s: marker = %q, want %q", row.name, mark, row.wantMark)
		}
		if style == nil {
			t.Errorf("%s: no style paired with the marker", row.name)
		}
		if _, ok := markerAlphabet[mark]; !ok {
			t.Errorf("%s: marker %q is not in the ui alphabet — use a ui.Mark* constant rather than an inline glyph", row.name, mark)
		}
		if other, clash := seen[mark]; clash {
			t.Errorf("%s renders %q, the same glyph as %s: the operator cannot tell the two states apart (BUG-10)", row.name, mark, other)
		}
		seen[mark] = row.name
	}
}
