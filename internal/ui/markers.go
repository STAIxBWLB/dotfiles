package ui

import (
	"os"
	"strings"

	"charm.land/lipgloss/v2"
)

// Marker names and their canonical glyphs live in one table. Partial (:) and
// preferred (+) are dot extensions; neither reuses a canonical state's glyph.
// Named strings preserve existing styled/concatenated report call sites and
// are resolved once at command entry, before any report goroutines start.
type markerID int

const (
	markerPresent markerID = iota
	markerFail
	markerWarn
	markerAbsent
	markerUnknown
	markerPending
	markerStarred
	markerNext
	markerPartial
	markerPreferred
	markerSection
	markerCount
)

type markerSpec struct {
	name, glyph, ascii string
	role               Role
}

var markerTable = [markerCount]markerSpec{
	markerPresent: {"ok", "✓", "ok", RoleSuccess}, markerFail: {"fail", "✗", "x", RoleError}, markerWarn: {"warn", "⚠", "!", RoleWarning},
	markerAbsent: {"absent", "-", "-", RoleHint}, markerUnknown: {"unknown", "?", "?", RoleHint}, markerPending: {"pending", "~", "~", RoleHint},
	markerStarred: {"active", "*", "*", RoleSuccess}, markerNext: {"next", "→", "->", RoleAccent},
	markerPartial: {"partial", ":", ":", RoleHint}, markerPreferred: {"preferred", "+", "+", RoleSuccess}, markerSection: {"section", "▸", ">", RoleSection},
}
var initialMarkers = markersFor(os.Getenv, false)

var (
	MarkPresent   = initialMarkers[markerPresent]
	MarkFail      = initialMarkers[markerFail]
	MarkWarn      = initialMarkers[markerWarn]
	MarkAbsent    = initialMarkers[markerAbsent]
	MarkUnknown   = initialMarkers[markerUnknown]
	MarkPending   = initialMarkers[markerPending]
	MarkStarred   = initialMarkers[markerStarred]
	MarkNext      = initialMarkers[markerNext]
	MarkPartial   = initialMarkers[markerPartial]
	MarkPreferred = initialMarkers[markerPreferred]
	MarkSection   = initialMarkers[markerSection]
)

type markerSet [markerCount]string

func captureMarkers() markerSet {
	return markerSet{MarkPresent, MarkFail, MarkWarn, MarkAbsent, MarkUnknown, MarkPending, MarkStarred, MarkNext, MarkPartial, MarkPreferred, MarkSection}
}
func (m markerSet) apply() {
	MarkPresent, MarkFail, MarkWarn, MarkAbsent, MarkUnknown, MarkPending, MarkStarred, MarkNext, MarkPartial, MarkPreferred, MarkSection = m[0], m[1], m[2], m[3], m[4], m[5], m[6], m[7], m[8], m[9], m[10]
}

// ASCIIMarkers follows locale precedence, including an explicitly selected
// non-UTF-8 locale overriding a lower-priority UTF-8 one.
func ASCIIMarkers(getenv func(string) string) bool {
	for _, key := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		if locale := getenv(key); locale != "" {
			return !strings.Contains(strings.ReplaceAll(strings.ToLower(locale), "-", ""), "utf8")
		}
	}
	return true
}
func markersFor(getenv func(string) string, ascii bool) markerSet {
	var out markerSet
	ascii = ascii || ASCIIMarkers(getenv)
	for i, marker := range markerTable {
		out[i] = marker.glyph
		if ascii {
			out[i] = marker.ascii
		}
	}
	return out
}

// MarkerColumn reserves exactly two cells, including ASCII ok and ->. Width
// measurement treats ambiguous characters as narrow, matching the standard.
func MarkerColumn(marker string) string {
	return marker + strings.Repeat(" ", max(0, 2-lipgloss.Width(marker)))
}

// RenderMarker pairs a raw named marker with its semantic role. A marker the
// caller has already styled is left intact. Only the marker argument is
// resolved; report values and raw payloads are never searched or rewritten.
func RenderMarker(marker string) string {
	current := captureMarkers()
	for i, spec := range markerTable {
		if marker == spec.glyph || marker == spec.ascii {
			return RoleStyle(spec.role).Render(current[i])
		}
	}
	return marker
}

// CanonicalMarkerGlyph returns the immutable UTF-8 glyph from the marker table,
// independent of the invocation's resolved Mark* strings. Unknown names return
// an empty string. This also gives conformance tests a stable expectation.
func CanonicalMarkerGlyph(name string) string {
	for _, spec := range markerTable {
		if spec.name == name {
			return spec.glyph
		}
	}
	return ""
}
