package ui

import (
	"fmt"
	"strings"
)

// CheckSummary counts measured report outcomes, never contextual fact rows.
// A view can display failures without turning them into an invocation failure.
type CheckSummary struct{ Failed, Warnings, OK, Unknown int }

func (s CheckSummary) Render() string {
	var parts []string
	for _, row := range []struct {
		count         int
		marker, label string
	}{
		{s.Failed, MarkFail, "failed"}, {s.Warnings, MarkWarn, "warnings"}, {s.OK, MarkPresent, "ok"}, {s.Unknown, MarkUnknown, "unknown"},
	} {
		if row.count > 0 {
			label := row.label
			if row.count == 1 && label == "warnings" {
				label = "warning"
			}
			parts = append(parts, fmt.Sprintf("%s %d %s", RenderMarker(row.marker), row.count, label))
		}
	}
	return strings.Join(parts, "  ")
}
