package ui

import (
	"bytes"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

func TestCheckSummaryOrderAndZeroOmission(t *testing.T) {
	restore, warning := SetPresentation(func(k string) string {
		if k == "LANG" {
			return "en_US.UTF-8"
		}
		return ""
	}, false)
	if warning != "" {
		t.Fatal(warning)
	}
	defer restore()
	var out bytes.Buffer
	w := NewColorWriter(&out, ColorNever, false)
	WriteReportLine(w, CheckSummary{Failed: 1, Warnings: 2, OK: 13, Unknown: 3}.Render())
	if got := out.String(); got != "✗ 1 failed  ⚠ 2 warnings  ✓ 13 ok  ? 3 unknown\n" {
		t.Fatalf("summary=%q", got)
	}
	out.Reset()
	WriteReportLine(w, CheckSummary{OK: 1}.Render())
	if out.String() != "✓ 1 ok\n" {
		t.Fatalf("zero counts shown: %q", out.String())
	}
	if (CheckSummary{}).Render() != "" {
		t.Fatal("no measurements became a synthetic pass")
	}
}

func TestBoundedReportKeepsIDAndStateColumnsIntact(t *testing.T) {
	t.Setenv("COLUMNS", "80")
	var out bytes.Buffer
	rows := []CheckRow{{Marker: MarkFail, ID: "identifier-" + strings.Repeat("x", 60), State: "failed", Detail: strings.Repeat("finding 한국어 ", 30)}}
	WriteCheckRows(NewColorWriter(&out, ColorNever, false), rows, false)
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) < 3 || !strings.Contains(lines[1], "...") || !strings.Contains(lines[1], "failed") {
		t.Fatalf("columns lost: %q", out.String())
	}
	for _, line := range lines {
		if width := lipgloss.Width(line); width > 80 {
			t.Errorf("line width=%d: %q", width, line)
		}
	}
	for _, line := range lines[2:] {
		if strings.Contains(line, "failed") || strings.Contains(line, "identifier") {
			t.Fatalf("fixed columns wrapped into detail: %q", line)
		}
	}
	if rows[0].ID != "identifier-"+strings.Repeat("x", 60) {
		t.Fatal("renderer mutated source rows")
	}
}

func TestReportWrapIsOptInOnPipeAndNeverTouchesJSON(t *testing.T) {
	text := strings.Repeat("x", 100)
	var out bytes.Buffer
	t.Setenv("COLUMNS", "")
	WriteReportLine(NewColorWriter(&out, ColorNever, false), text)
	if out.String() != text+"\n" {
		t.Fatal("unrequested pipe wrapping")
	}
	t.Setenv("COLUMNS", "80")
	out.Reset()
	WriteReportLine(NewColorWriter(&out, ColorNever, false), text)
	for _, line := range strings.Split(out.String(), "\n") {
		if lipgloss.Width(line) > 80 {
			t.Fatal("long word exceeded requested width")
		}
	}
	payload := `{"value":"` + text + `"}`
	out.Reset()
	WriteReportLine(NewColorWriter(&out, ColorAlways, true), payload)
	if out.String() != payload+"\n" {
		t.Fatalf("JSON changed: %q", out.String())
	}
}
