package ui

import (
	"bytes"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestMarkerLocalePrecedence(t *testing.T) {
	for _, tc := range []struct {
		name  string
		env   map[string]string
		ascii bool
	}{
		{"LANG C", map[string]string{"LANG": "C"}, true},
		{"POSIX", map[string]string{"LANG": "POSIX"}, true},
		{"unset", nil, true},
		{"UTF8", map[string]string{"LANG": "ko_KR.UTF-8"}, false},
		{"UTF8 compact", map[string]string{"LANG": "en_US.utf8"}, false},
		{"LC_ALL overrides UTF8", map[string]string{"LC_ALL": "C", "LC_CTYPE": "en_US.UTF-8", "LANG": "en_US.UTF-8"}, true},
		{"LC_CTYPE overrides LANG", map[string]string{"LC_CTYPE": "C", "LANG": "en_US.UTF-8"}, true},
		{"LC_ALL UTF8 overrides C", map[string]string{"LC_ALL": "C.UTF-8", "LANG": "C"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ASCIIMarkers(func(k string) string { return tc.env[k] }); got != tc.ascii {
				t.Fatalf("ASCII=%v,want %v", got, tc.ascii)
			}
		})
	}
}

func TestMarkerAlphabetAndTwoCellColumns(t *testing.T) {
	unicode := markersFor(func(k string) string {
		if k == "LANG" {
			return "en_US.UTF-8"
		}
		return ""
	}, false)
	ascii := markersFor(func(string) string { return "C" }, false)
	forced := markersFor(func(string) string { return "en_US.UTF-8" }, true)
	if ascii != forced {
		t.Fatal("--ascii did not override UTF8 locale")
	}
	if unicode[markerAbsent] != "-" || unicode[markerStarred] != "*" || unicode[markerUnknown] != "?" || unicode[markerNext] != "→" {
		t.Fatalf("wrong canonical states: %v", unicode)
	}
	for _, m := range []markerSet{unicode, ascii} {
		seen := map[string]bool{}
		for _, mark := range m {
			if seen[mark] {
				t.Errorf("ambiguous duplicate marker %q", mark)
			}
			seen[mark] = true
			if strings.ContainsAny(mark, "★◆·") {
				t.Errorf("ambiguous legacy marker %q", mark)
			}
			if width := lipgloss.Width(MarkerColumn(StyleSuccess.Render(mark))); width != 2 {
				t.Errorf("marker %q width=%d", mark, width)
			}
		}
	}
	for _, mark := range ascii {
		for _, r := range mark {
			if r > 127 {
				t.Errorf("non-ASCII fallback %q", mark)
			}
		}
	}
	if ascii[markerPresent] != "ok" || ascii[markerFail] != "x" || ascii[markerWarn] != "!" || ascii[markerNext] != "->" {
		t.Fatalf("wrong ASCII states: %v", ascii)
	}
}

func TestMarkerSelectionScopedWithoutRewritingValues(t *testing.T) {
	before := captureMarkers()
	restore, err := SetPresentation(func(k string) string {
		if k == "LANG" {
			return "C"
		}
		return ""
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	WriteKV(NewColorWriter(&out, ColorNever, false), "Path", "some→path")
	if !strings.Contains(out.String(), "some→path") {
		t.Fatal("ASCII marker selection rewrote user values")
	}
	if MarkPresent != "ok" || MarkFail != "x" || MarkSection != ">" {
		t.Fatal("locale did not reach named marker call sites")
	}
	restore()
	if captureMarkers() != before {
		t.Fatal("marker selection escaped invocation")
	}
}

func TestRawMarkersUseTheirSemanticRoles(t *testing.T) {
	restore, err := SetPresentation(func(k string) string {
		if k == "LANG" {
			return "C"
		}
		return ""
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	defer restore()
	current := captureMarkers()
	for i, spec := range markerTable {
		want := RoleStyle(spec.role).Render(current[i])
		if got := RenderMarker(spec.glyph); got != want {
			t.Fatalf("marker %q role=%s: %q != %q", spec.glyph, spec.role, got, want)
		}
		if got := RenderMarker(want); got != want {
			t.Fatalf("styled marker changed: %q", got)
		}
	}
}
