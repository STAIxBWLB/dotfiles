package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/STAIxBWLB/dotfiles/internal/syncer"
)

// #225: the plan points to `dot sync names trim` only for workspace names; a
// mirror-only name gets its own section and advice.
func TestPrintPushPlan_UnsupportedSections(t *testing.T) {
	var out bytes.Buffer
	printPushPlan(&Printer{Out: &out}, &syncer.PushPlan{
		Unsupported:       []string{"keep./b.md"},
		Leftovers:         []string{"old./c.md"},
		MirrorUnsupported: []string{"clients/Acme Inc./contract.pdf"},
		MoveLeftovers:     true,
	})
	text := out.String()
	for _, want := range []string{
		"Unsupported names: 1", "Mirror leftovers: 1", "Mirror-only unsupported names: 1",
		"The push moves them into the workspace's .sync-conflicts/<ts>/from-mirror/",
		"they may be cloud files, so they are listed only",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("plan output lacks %q:\n%s", want, text)
		}
	}
	if strings.Count(text, "dot sync names trim") != 1 || strings.Index(text, "dot sync names trim") > strings.Index(text, "Mirror leftovers") {
		t.Errorf("`dot sync names trim` should appear once, for workspace names only:\n%s", text)
	}
}
